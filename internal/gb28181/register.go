package gb28181

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	coreauth "github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
)

// GB28181API is the core API for GB28181 signaling.
type GB28181API struct {
	cfg          *Config
	store        *DeviceStore
	streams      *streamsManager
	svr          *Server
	client       *sipgo.Client
	mediaEngine  media.Engine
	hub          *WSHub
	digestNonces *coreauth.DigestNonceStore
}

// NewGB28181API creates a new GB28181API instance.
func NewGB28181API(cfg *Config, store *DeviceStore, client *sipgo.Client, mediaEngine media.Engine) *GB28181API {
	return &GB28181API{
		cfg:          cfg,
		store:        store,
		streams:      globalStreams,
		client:       client,
		mediaEngine:  mediaEngine,
		hub:          store.hub,
		digestNonces: coreauth.NewDigestNonceStore(5*time.Minute, 8192),
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
	if strings.TrimSpace(password) == "" || strings.TrimSpace(password) == "#" {
		slog.Error("[SIP] REGISTER rejected - no GB28181 password configured", "device_id", deviceID)
		tx.Respond(sip.NewResponseFromRequest(req, 403, "GB28181 authentication is not configured", nil))
		return
	}
	if req.GetHeader("Authorization") == nil || !g.verifyDigestAuth(req, password, g.cfg.GetDomain()) {
		if req.GetHeader("Authorization") != nil {
			slog.Warn("[SIP] REGISTER rejected - auth failed", "device_id", deviceID)
		}
		g.respondDigestChallenge(req, tx)
		return
	}
	slog.Info("[SIP] REGISTER auth success", "device_id", deviceID)

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

func (g *GB28181API) respondDigestChallenge(req *sip.Request, tx sip.ServerTransaction) {
	nonce, err := g.digestNonces.Issue()
	if err != nil {
		slog.Error("[SIP] failed to create digest nonce", "error", err)
		tx.Respond(sip.NewResponseFromRequest(req, 500, "Server Error", nil))
		return
	}
	res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
	res.AppendHeader(sip.NewHeader("WWW-Authenticate", fmt.Sprintf(
		`Digest realm="%s",qop="auth",algorithm=MD5,nonce="%s"`, g.cfg.GetDomain(), nonce)))
	tx.Respond(res)
}

func (g *GB28181API) verifyDigestAuth(req *sip.Request, password, realm string) bool {
	authHdr := req.GetHeader("Authorization")
	if authHdr == nil {
		return false
	}
	params, ok := parseDigestAuthorization(authHdr.Value())
	if !ok {
		return false
	}
	username := params["username"]
	nonce := params["nonce"]
	uri := params["uri"]
	response := params["response"]
	qop := strings.ToLower(params["qop"])
	nc := params["nc"]
	cnonce := params["cnonce"]
	if username == "" || username != req.From().Address.User ||
		params["realm"] != realm || nonce == "" || uri != req.Recipient.String() ||
		!strings.EqualFold(params["algorithm"], "MD5") || qop != "auth" || cnonce == "" ||
		len(nc) != 8 || len(response) != 32 {
		return false
	}
	nonceCount, err := strconv.ParseUint(nc, 16, 32)
	if err != nil || nonceCount == 0 {
		return false
	}
	if !coreauth.VerifyDigest(req.Method.String(), username, realm, password, params) {
		return false
	}
	return g.digestNonces.Consume(nonce)
}

func parseDigestAuthorization(header string) (map[string]string, bool) {
	return coreauth.ParseDigestAuthorization(header)
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
