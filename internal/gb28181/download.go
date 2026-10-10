package gb28181

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// DownloadSession represents an active recording download session.
type DownloadSession struct {
	ID          int64
	DeviceID    string
	ChannelID   string
	StartTime   time.Time
	EndTime     time.Time
	FilePath    string
	SessionID   string
	SSRC        string
	Status      string
	client      *sipgo.Client
	cfg         *Config
	mediaEngine media.Engine
	store       storage.GB28181DownloadRepository
}

// DownloadManager manages recording download sessions.
type DownloadManager struct {
	sessions map[string]*DownloadSession
	client   *sipgo.Client
	cfg      *Config
	mediaEng media.Engine
	store    storage.GB28181DownloadRepository
	dataDir  string
}

// NewDownloadManager creates a new download manager.
func NewDownloadManager(client *sipgo.Client, cfg *Config, mediaEng media.Engine, store storage.GB28181DownloadRepository, dataDir string) *DownloadManager {
	return &DownloadManager{
		sessions: make(map[string]*DownloadSession),
		client:   client,
		cfg:      cfg,
		mediaEng: mediaEng,
		store:    store,
		dataDir:  dataDir,
	}
}

// StartDownload starts a recording download from a device.
func (dm *DownloadManager) StartDownload(deviceID, channelID string, startTime, endTime time.Time, deviceStore *DeviceStore) (*DownloadSession, error) {
	ch, ok := deviceStore.GetChannel(deviceID, channelID)
	if !ok {
		return nil, ErrChannelNotExist
	}

	if !ch.device.IsOnline {
		return nil, ErrDeviceOffline
	}

	// Create download directory
	downloadDir := filepath.Join(dm.dataDir, "downloads", deviceID)
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		return nil, fmt.Errorf("create download dir failed: %w", err)
	}

	fileName := fmt.Sprintf("%s_%s_%s.mp4",
		channelID,
		startTime.Format("20060102_150405"),
		endTime.Format("20060102_150405"))
	filePath := filepath.Join(downloadDir, fileName)

	// Record in database
	dlRow := &storage.DownloadRecordRow{
		DeviceID:  deviceID,
		ChannelID: channelID,
		FilePath:  filePath,
		StartTime: startTime,
		EndTime:   endTime,
		Status:    "downloading",
	}
	id, err := dm.store.CreateDownload(context.Background(), dlRow)
	if err != nil {
		return nil, fmt.Errorf("create download record failed: %w", err)
	}

	// Start RTP receive for download stream
	streamID := fmt.Sprintf("download_%s_%s_%d", deviceID, channelID, id)
	protocol := "udp"

	rtpSession, err := dm.mediaEng.StartRTPReceive(context.Background(), media.StartRTPReceiveRequest{
		StreamID: streamID,
		AppName:  "rtp",
		Protocol: protocol,
		Timeout:  30 * time.Second,
	})
	if err != nil {
		_ = dm.store.UpdateDownloadStatus(context.Background(), id, "failed", 0)
		return nil, fmt.Errorf("open RTP server failed: %w", err)
	}

	// Send SIP INVITE for download
	ssrc, err := dm.sipDownloadInvite(ch, channelID, rtpSession.Port, startTime, endTime)
	if err != nil {
		_ = dm.mediaEng.StopRTPReceive(context.Background(), rtpSession.SessionID)
		_ = dm.store.UpdateDownloadStatus(context.Background(), id, "failed", 0)
		return nil, fmt.Errorf("download INVITE failed: %w", err)
	}

	ds := &DownloadSession{
		ID:          id,
		DeviceID:    deviceID,
		ChannelID:   channelID,
		StartTime:   startTime,
		EndTime:     endTime,
		FilePath:    filePath,
		SessionID:   rtpSession.SessionID,
		SSRC:        ssrc,
		Status:      "downloading",
		client:      dm.client,
		cfg:         dm.cfg,
		mediaEngine: dm.mediaEng,
		store:       dm.store,
	}

	key := fmt.Sprintf("%s:%s:%d", deviceID, channelID, id)
	dm.sessions[key] = ds

	slog.Info("[Download] started",
		"device_id", deviceID,
		"channel_id", channelID,
		"file_path", filePath,
		"ssrc", ssrc,
		"id", id,
	)

	return ds, nil
}

func (dm *DownloadManager) sipDownloadInvite(ch *Channel, channelID string, port int, startTime, endTime time.Time) (string, error) {
	ipStr := dm.cfg.MediaIP
	domain := dm.cfg.GetDomain()
	ssrc := fmt.Sprintf("0%s%04d", domain[3:8], randInt(0, 8999))

	body := buildDownloadSDP(ch.device.DeviceID, channelID, ipStr, port, 0, ssrc, startTime, endTime)

	dev := ch.device
	recipient := sip.Uri{
		Scheme: "sip",
		User:   channelID,
		Host:   getHost(dev.Address),
		Port:   getPort(dev.Address),
	}

	req := sip.NewRequest(sip.INVITE, recipient)
	req.SetBody(body)
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.AppendHeader(sip.NewHeader("Subject",
		fmt.Sprintf("%s:%s,%s:%s", channelID, ssrc, dm.cfg.ID, "0")))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := dm.client.TransactionRequest(ctx, req)
	if err != nil {
		return "", fmt.Errorf("INVITE send failed: %w", err)
	}
	defer tx.Terminate()

	for {
		select {
		case res := <-tx.Responses():
			if res.IsProvisional() {
				continue
			}
			if res.IsSuccess() {
				// Send ACK
				ackRecipient := req.Recipient
				if contact := res.GetHeader("Contact"); contact != nil {
					var contactUri sip.Uri
					if err := sip.ParseUri(contact.Value(), &contactUri); err == nil {
						ackRecipient = contactUri
					}
				}

				ack := sip.NewRequest(sip.ACK, ackRecipient)
				ack.AppendHeader(sip.HeaderClone(req.From()))
				ack.AppendHeader(sip.HeaderClone(res.To()))
				ack.AppendHeader(sip.HeaderClone(req.CallID()))
				cseq := *req.CSeq()
				cseq.MethodName = sip.ACK
				ack.AppendHeader(&cseq)
				_ = dm.client.WriteRequest(ack)

				return ssrc, nil
			}
			return "", fmt.Errorf("INVITE rejected: %d %s", res.StatusCode, res.Reason)
		case <-ctx.Done():
			return "", fmt.Errorf("INVITE timeout")
		}
	}
}

// StopDownload stops a download session.
func (dm *DownloadManager) StopDownload(deviceID, channelID string, id int64) error {
	key := fmt.Sprintf("%s:%s:%d", deviceID, channelID, id)
	ds, ok := dm.sessions[key]
	if !ok {
		return nil
	}

	if ds.SessionID != "" && dm.mediaEng != nil {
		_ = dm.mediaEng.StopRTPReceive(context.Background(), ds.SessionID)
	}

	_ = dm.store.UpdateDownloadStatus(context.Background(), ds.ID, "completed", 0)
	delete(dm.sessions, key)

	slog.Info("[Download] stopped", "device_id", deviceID, "channel_id", channelID, "id", id)
	return nil
}

// GetDownloadProgress returns the download progress.
func (dm *DownloadManager) GetDownloadProgress(id int64) (*storage.DownloadRecordRow, error) {
	return dm.store.GetDownload(context.Background(), id)
}

func buildDownloadSDP(deviceID, channelID, ip string, port int, streamMode int8, ssrc string, startTime, endTime time.Time) []byte {
	protocol := "RTP/AVP"
	if streamMode == 1 || streamMode == 2 {
		protocol = "TCP/RTP/AVP"
	}

	msg := fmt.Sprintf(`v=0
o=%s 0 0 IN IP4 %s
s=Download
u=%s:0
c=IN IP4 %s
t=%d %d
m=video %d %s 96
a=recvonly
a=rtpmap:96 PS/90000
y=%s
f=v/2/5/25/1/25000a/1/8/1
`, deviceID, ip, channelID, ip,
		startTime.Unix(), endTime.Unix(),
		port, protocol, ssrc)

	return []byte(msg)
}
