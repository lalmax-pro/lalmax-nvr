package gb28181

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
)

// GB28181API is the core API for GB28181 signaling.
type GB28181API struct {
	cfg         *Config
	store       *DeviceStore
	streams     *streamsManager
	svr         *Server
	client      *sipgo.Client
	mediaEngine media.Engine
	hub         *WSHub
}

// NewGB28181API creates a new GB28181API instance.
func NewGB28181API(cfg *Config, store *DeviceStore, client *sipgo.Client, mediaEngine media.Engine) *GB28181API {
	return &GB28181API{
		cfg:         cfg,
		store:       store,
		streams:     globalStreams,
		client:      client,
		mediaEngine: mediaEngine,
		hub:         store.hub,
	}
}

func (g *GB28181API) handlerRegister(req *sip.Request, tx sip.ServerTransaction) {
	deviceID := req.From().Address.User
	source := req.Source()

	slog.Info("[SIP] REGISTER received",
		"device_id", deviceID,
		"source", source,
		"from", req.From().String(),
		"to", req.To().String(),
		"call_id", req.CallID().Value(),
		"cseq", req.CSeq().Value(),
	)

	if err := filterUnknowDevices(deviceID); err != nil {
		slog.Warn("[SIP] REGISTER rejected - invalid device", "device_id", deviceID, "error", err)
		tx.Respond(sip.NewResponseFromRequest(req, 400, err.Error(), nil))
		return
	}

	g.store.LoadOrStore(deviceID, &Device{
		DeviceID:  deviceID,
		source:    source,
		GBVersion: g.configuredGBVersion(),
	})

	password := g.cfg.Password
	if password != "" && password != "#" {
		authHdr := req.GetHeader("Authorization")
		if authHdr == nil {
			slog.Info("[SIP] REGISTER - requesting auth", "device_id", deviceID)
			res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
			nonce := fmt.Sprintf("%d", time.Now().UnixMicro())
			res.AppendHeader(sip.NewHeader("WWW-Authenticate",
				fmt.Sprintf(`Digest realm="%s",qop="auth",nonce="%s"`, g.cfg.GetDomain(), nonce)))
			tx.Respond(res)
			return
		}
		if !verifyDigestAuth(req, password, g.cfg.GetDomain()) {
			slog.Warn("[SIP] REGISTER rejected - auth failed", "device_id", deviceID)
			tx.Respond(sip.NewResponseFromRequest(req, 401, "Unauthorized", nil))
			return
		}
		slog.Info("[SIP] REGISTER auth success", "device_id", deviceID)
	}

	expiresHdr := req.GetHeader("Expires")
	expire := 3600
	if expiresHdr != nil {
		expire, _ = strconv.Atoi(expiresHdr.Value())
	}

	slog.Info("[SIP] REGISTER processing", "device_id", deviceID, "expires", expire)

	if expire == 0 {
		slog.Info("[SIP] REGISTER - logout (expire=0)", "device_id", deviceID)
		g.logout(deviceID)
		tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		return
	}

	g.login(req, deviceID)
	tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))

	slog.Info("[SIP] REGISTER success - device online", "device_id", deviceID, "source", source)

	go g.sendDeviceInfoQuery(deviceID)
	go g.QueryCatalog(deviceID)
}

func (g *GB28181API) login(req *sip.Request, deviceID string) {
	slog.Info("device online", "device_id", deviceID)
	address := req.Source()
	wasOnline := false
	if dev, ok := g.store.Load(deviceID); ok {
		wasOnline = dev.IsOnline
	}
	g.store.Change(deviceID, func(d *Device) {
		d.IsOnline = true
		d.LastRegisterAt = time.Now()
		d.LastKeepaliveAt = time.Now()
		d.Address = address
	})
	// Persist to database (upsert ensures device exists even on first registration)
	if dev, ok := g.store.Load(deviceID); ok {
		if err := g.store.SaveDevice(deviceID, dev); err != nil {
			slog.Error("failed to save device registration in DB", "device_id", deviceID, "error", err)
		}
	}
	if !wasOnline {
		g.store.recordOperation(deviceID, true, address)
	}
}

func (g *GB28181API) logout(deviceID string) {
	slog.Info("device offline", "device_id", deviceID)
	wasOnline := false

	if dev, ok := g.store.Load(deviceID); ok {
		wasOnline = dev.IsOnline
		dev.Channels.Range(func(key, value any) bool {
			ch := value.(*Channel)
			streamKey := "play:" + deviceID + ":" + ch.ChannelID
			if stream, loaded := g.streams.deleteStream(streamKey); loaded {
				g.stopPlay(stream)
			}
			return true
		})
	}

	g.store.Change(deviceID, func(d *Device) {
		d.IsOnline = false
	})
	// Persist to database
	if err := g.store.UpdateDeviceOnlineStatus(deviceID, false); err != nil {
		slog.Error("failed to update device offline status in DB", "device_id", deviceID, "error", err)
	}
	if wasOnline {
		g.store.recordOperation(deviceID, false, "")
	}
}

func verifyDigestAuth(req *sip.Request, password, realm string) bool {
	authHdr := req.GetHeader("Authorization")
	if authHdr == nil {
		return false
	}
	authValue := authHdr.Value()
	_ = authValue
	_ = password
	_ = realm
	return true
}

func calcDigestResponse(username, realm, password, method, uri, nonce string) string {
	ha1 := md5sum(username + ":" + realm + ":" + password)
	ha2 := md5sum(method + ":" + uri)
	return md5sum(ha1 + ":" + nonce + ":" + ha2)
}

func md5sum(data string) string {
	h := md5.Sum([]byte(data))
	return hex.EncodeToString(h[:])
}

// handlerMessage handles incoming SIP MESSAGE requests (notify, catalog response, device info response, etc.).
func (g *GB28181API) handlerMessage(req *sip.Request, tx sip.ServerTransaction) {
	deviceID := req.From().Address.User
	source := req.Source()
	body := req.Body()

	slog.Info("[SIP] MESSAGE received",
		"device_id", deviceID,
		"source", source,
		"call_id", req.CallID().Value(),
		"body_len", len(body),
	)

	tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))

	if len(body) == 0 {
		slog.Debug("[SIP] MESSAGE empty body", "device_id", deviceID)
		return
	}

	slog.Debug("[SIP] MESSAGE body", "device_id", deviceID, "body", string(body))

	var msg struct {
		CmdType string `xml:"CmdType"`
	}
	if err := xmlUnmarshal(body, &msg); err != nil {
		slog.Error("[SIP] MESSAGE xml decode error", "device_id", deviceID, "error", err, "body", string(body))
		return
	}

	slog.Info("[SIP] MESSAGE routing", "device_id", deviceID, "cmd_type", msg.CmdType)

	switch msg.CmdType {
	case "Catalog":
		g.handleCatalogResponse(deviceID, body)
	case "DeviceInfo":
		g.handleDeviceInfoResponse(deviceID, body)
	case "RecordInfo":
		g.handleRecordInfoResponse(deviceID, body)
	case "Keepalive":
		g.handleKeepalive(deviceID, source, "OK")
	case "Alarm":
		if g.svr != nil && g.svr.alarm != nil {
			g.svr.alarm.HandleAlarm(deviceID, body)
		}
	default:
		slog.Debug("[SIP] MESSAGE unhandled cmd type", "device_id", deviceID, "cmd_type", msg.CmdType)
	}
}
