package gb28181

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// PlatformConfig holds configuration for a single upstream platform.
type PlatformConfig struct {
	ID              int64  `yaml:"id"`
	Name            string `yaml:"name"`
	Enable          bool   `yaml:"enable"`
	ServerGBID      string `yaml:"server_gb_id"`
	ServerGBDomain  string `yaml:"server_gb_domain"`
	ServerIP        string `yaml:"server_ip"`
	ServerPort      int    `yaml:"server_port"`
	DeviceGBID      string `yaml:"device_gb_id"`
	DeviceGBDomain  string `yaml:"device_gb_domain"`
	DeviceIP        string `yaml:"device_ip"`
	DevicePort      int    `yaml:"device_port"`
	Username        string `yaml:"username"`
	Password        string `yaml:"password"`
	Transport       string `yaml:"transport"`
	CharacterSet    string `yaml:"character_set"`
	Expires         int    `yaml:"expires"`
	KeepTimeout     int    `yaml:"keep_timeout"`
	MaxTimeoutCount int    `yaml:"max_timeout_count"`
}

// Platform represents a runtime instance of an upstream platform connection.
type Platform struct {
	mu             sync.Mutex
	Config         *PlatformConfig
	client         *sipgo.Client
	sipIP          string
	mediaIP        string
	serial         string
	password       string
	sn             int
	status         bool
	keepAliveReply int
	registerCallID string
	quit           chan struct{}
	store          *storage.DB
}

// PlatformManager manages all upstream platform connections.
type PlatformManager struct {
	mu        sync.RWMutex
	platforms map[int64]*Platform
	client    *sipgo.Client
	sipIP     string
	mediaIP   string
	serial    string
	password  string
	store     *storage.DB
}

// NewPlatformManager creates a new platform manager.
func NewPlatformManager(client *sipgo.Client, sipIP, mediaIP, serial, password string, store *storage.DB) *PlatformManager {
	return &PlatformManager{
		platforms: make(map[int64]*Platform),
		client:    client,
		sipIP:     sipIP,
		mediaIP:   mediaIP,
		serial:    serial,
		password:  password,
		store:     store,
	}
}

// LoadPlatforms loads all platforms from database and starts enabled ones.
func (pm *PlatformManager) LoadPlatforms() error {
	ctx := context.Background()
	rows, err := pm.store.ListPlatforms(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		cfg := &PlatformConfig{
			ID:              row.ID,
			Name:            row.Name,
			Enable:          row.Enable,
			ServerGBID:      row.ServerGBID,
			ServerGBDomain:  row.ServerGBDomain,
			ServerIP:        row.ServerIP,
			ServerPort:      row.ServerPort,
			DeviceGBID:      row.DeviceGBID,
			DeviceGBDomain:  row.DeviceGBDomain,
			DeviceIP:        row.DeviceIP,
			DevicePort:      row.DevicePort,
			Username:        row.Username,
			Password:        row.Password,
			Transport:       row.Transport,
			CharacterSet:    row.CharacterSet,
			Expires:         row.Expires,
			KeepTimeout:     row.KeepTimeout,
			MaxTimeoutCount: row.MaxTimeoutCount,
		}
		if cfg.Expires == 0 {
			cfg.Expires = 3600
		}
		if cfg.KeepTimeout == 0 {
			cfg.KeepTimeout = 60
		}
		if cfg.MaxTimeoutCount == 0 {
			cfg.MaxTimeoutCount = 3
		}
		if cfg.Transport == "" {
			cfg.Transport = "UDP"
		}
		if cfg.CharacterSet == "" {
			cfg.CharacterSet = "GB2312"
		}

		p := &Platform{
			Config:   cfg,
			client:   pm.client,
			sipIP:    pm.sipIP,
			mediaIP:  pm.mediaIP,
			serial:   pm.serial,
			password: pm.password,
			store:    pm.store,
			quit:     make(chan struct{}),
		}

		pm.mu.Lock()
		pm.platforms[cfg.ID] = p
		pm.mu.Unlock()

		if cfg.Enable {
			go p.Start()
		}
	}
	slog.Info("Loaded GB28181 platforms", "count", len(rows))
	return nil
}

// AddPlatform adds a new platform and optionally starts it.
func (pm *PlatformManager) AddPlatform(cfg *PlatformConfig) error {
	ctx := context.Background()
	id, err := pm.store.CreatePlatform(ctx, &storage.PlatformRow{
		Name:            cfg.Name,
		Enable:          cfg.Enable,
		ServerGBID:      cfg.ServerGBID,
		ServerGBDomain:  cfg.ServerGBDomain,
		ServerIP:        cfg.ServerIP,
		ServerPort:      cfg.ServerPort,
		DeviceGBID:      cfg.DeviceGBID,
		DeviceGBDomain:  cfg.DeviceGBDomain,
		DeviceIP:        cfg.DeviceIP,
		DevicePort:      cfg.DevicePort,
		Username:        cfg.Username,
		Password:        cfg.Password,
		Transport:       cfg.Transport,
		CharacterSet:    cfg.CharacterSet,
		Expires:         cfg.Expires,
		KeepTimeout:     cfg.KeepTimeout,
		MaxTimeoutCount: cfg.MaxTimeoutCount,
	})
	if err != nil {
		return err
	}
	cfg.ID = id

	p := &Platform{
		Config:   cfg,
		client:   pm.client,
		sipIP:    pm.sipIP,
		mediaIP:  pm.mediaIP,
		serial:   pm.serial,
		password: pm.password,
		store:    pm.store,
		quit:     make(chan struct{}),
	}

	pm.mu.Lock()
	pm.platforms[id] = p
	pm.mu.Unlock()

	if cfg.Enable {
		go p.Start()
	}
	return nil
}

// RemovePlatform stops and removes a platform.
func (pm *PlatformManager) RemovePlatform(id int64) error {
	pm.mu.Lock()
	p, ok := pm.platforms[id]
	if ok {
		p.Stop()
		delete(pm.platforms, id)
	}
	pm.mu.Unlock()

	return pm.store.DeletePlatform(context.Background(), id)
}

// GetPlatform returns a platform by ID.
func (pm *PlatformManager) GetPlatform(id int64) (*Platform, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	p, ok := pm.platforms[id]
	return p, ok
}

// ListPlatforms returns all platform runtime states.
func (pm *PlatformManager) ListPlatforms() []*Platform {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	var result []*Platform
	for _, p := range pm.platforms {
		result = append(result, p)
	}
	return result
}

// GetSharedChannels returns shared channels for a platform.
func (pm *PlatformManager) GetSharedChannels(platformID int64) []PlatformChannel {
	ctx := context.Background()
	shared := true
	rows, err := pm.store.ListPlatformChannels(ctx, platformID, &shared)
	if err != nil {
		slog.Error("failed to list shared channels", "platform_id", platformID, "error", err)
		return nil
	}
	var channels []PlatformChannel
	for _, row := range rows {
		channels = append(channels, PlatformChannel{
			ChannelID:  row.ChannelID,
			DeviceID:   row.DeviceID,
			CustomID:   row.CustomID,
			CustomName: row.CustomName,
			StreamPath: row.StreamPath,
		})
	}
	return channels
}

// PlatformChannel represents a channel shared with an upstream platform.
type PlatformChannel struct {
	ChannelID  string
	DeviceID   string
	CustomID   string
	CustomName string
	StreamPath string
}

// Start starts the platform registration and keepalive loop.
func (p *Platform) Start() {
	slog.Info("[Platform] starting", "name", p.Config.Name, "server_gb_id", p.Config.ServerGBID)

	// Initial register with retry
	for {
		if err := p.Register(); err != nil {
			slog.Error("[Platform] register failed, retrying", "name", p.Config.Name, "error", err)
			select {
			case <-time.After(time.Duration(p.Config.Expires/2) * time.Second):
				continue
			case <-p.quit:
				return
			}
		}
		break
	}

	// Start keepalive loop
	keepTicker := time.NewTicker(time.Duration(p.Config.KeepTimeout) * time.Second)
	defer keepTicker.Stop()

	// Re-register before expiry
	regTicker := time.NewTicker(time.Duration(p.Config.Expires*3/4) * time.Second)
	defer regTicker.Stop()

	for {
		select {
		case <-keepTicker.C:
			if err := p.Keepalive(); err != nil {
				p.keepAliveReply++
				slog.Error("[Platform] keepalive failed", "name", p.Config.Name, "error", err, "count", p.keepAliveReply)
				if p.keepAliveReply >= p.Config.MaxTimeoutCount {
					slog.Warn("[Platform] max keepalive retries reached, re-registering", "name", p.Config.Name)
					p.status = false
					p.keepAliveReply = 0
					_ = p.store.UpdatePlatformStatus(context.Background(), p.Config.ID, false)
					_ = p.Register()
				}
			} else {
				p.keepAliveReply = 0
			}
		case <-regTicker.C:
			if err := p.Register(); err != nil {
				slog.Error("[Platform] re-register failed", "name", p.Config.Name, "error", err)
			}
		case <-p.quit:
			return
		}
	}
}

// Stop stops the platform.
func (p *Platform) Stop() {
	cfg := p.Config
	if cfg != nil && p.status {
		// Record unregister event
		_ = p.store.AddPlatformEvent(context.Background(), storage.PlatformEventRow{
			PlatformID:   cfg.ID,
			PlatformName: cfg.Name,
			EventType:    storage.PlatformEventUnregister,
			ServerIP:     cfg.ServerIP,
			ServerPort:   cfg.ServerPort,
			Details:      "platform stopped",
		})
	}
	p.status = false
	select {
	case <-p.quit:
	default:
		close(p.quit)
	}
}

// Register sends SIP REGISTER to the upstream platform.
func (p *Platform) Register() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	cfg := p.Config
	recipient := sip.Uri{
		Scheme: "sip",
		User:   cfg.ServerGBID,
		Host:   cfg.ServerIP,
		Port:   cfg.ServerPort,
	}

	req := sip.NewRequest(sip.REGISTER, recipient)
	req.SetTransport(cfg.Transport)

	callID := fmt.Sprintf("%s-%d@%s", cfg.DeviceGBID, time.Now().Unix(), cfg.DeviceIP)
	req.AppendHeader(sip.NewHeader("Call-ID", callID))

	cseq := uint32(p.sn)
	p.sn++
	req.AppendHeader(&sip.CSeqHeader{SeqNo: cseq, MethodName: sip.REGISTER})

	domain := cfg.DeviceGBDomain
	if domain == "" {
		domain = cfg.DeviceGBID
		if len(domain) > 10 {
			domain = domain[:10]
		}
	}
	req.AppendHeader(&sip.FromHeader{
		Address: sip.Uri{User: cfg.DeviceGBID, Host: domain},
	})
	req.AppendHeader(&sip.ToHeader{
		Address: sip.Uri{User: cfg.DeviceGBID, Host: domain},
	})

	serverDomain := cfg.ServerGBDomain
	if serverDomain == "" {
		serverDomain = cfg.ServerGBID
		if len(serverDomain) > 10 {
			serverDomain = serverDomain[:10]
		}
	}

	req.AppendHeader(&sip.ContactHeader{
		Address: sip.Uri{User: cfg.DeviceGBID, Host: cfg.DeviceIP, Port: cfg.DevicePort},
	})
	req.AppendHeader(sip.NewHeader("Expires", fmt.Sprintf("%d", cfg.Expires)))
	maxFwd := sip.MaxForwardsHeader(70)
	req.AppendHeader(&maxFwd)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := p.client.TransactionRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("register send failed: %w", err)
	}
	defer tx.Terminate()

	res := <-tx.Responses()
	if res == nil {
		return fmt.Errorf("register no response")
	}

	// Handle 401 challenge
	if res.StatusCode == 401 {
		wwwAuth := res.GetHeader("WWW-Authenticate")
		if wwwAuth == nil {
			return fmt.Errorf("no auth challenge")
		}

		slog.Debug("[Platform] received auth challenge", "name", cfg.Name, "header", wwwAuth.Value())

		// Simple digest auth
		nonce := extractNonce(wwwAuth.Value())
		uri := fmt.Sprintf("sip:%s", serverDomain)
		response := calcDigestResponse(cfg.Username, serverDomain, cfg.Password, "REGISTER", uri, nonce)

		newReq := req.Clone()
		newReq.RemoveHeader("Via")
		authHeader := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm=MD5`,
			cfg.Username, serverDomain, nonce, uri, response)
		newReq.AppendHeader(sip.NewHeader("Authorization", authHeader))
		cseq2 := uint32(p.sn)
		p.sn++
		newReq.AppendHeader(&sip.CSeqHeader{SeqNo: cseq2, MethodName: sip.REGISTER})

		transport := cfg.Transport
		if transport == "" {
			transport = "UDP"
		}
		viaHeader := &sip.ViaHeader{
			ProtocolName:    "SIP",
			ProtocolVersion: "2.0",
			Transport:       transport,
			Host:            cfg.DeviceIP,
			Port:            cfg.DevicePort,
			Params:          sip.HeaderParams(sip.NewParams()),
		}
		viaHeader.Params.Add("branch", sip.GenerateBranchN(16))
		newReq.PrependHeader(viaHeader)

		tx2, err := p.client.TransactionRequest(ctx, newReq)
		if err != nil {
			return fmt.Errorf("register auth send failed: %w", err)
		}
		defer tx2.Terminate()

		res = <-tx2.Responses()
		if res == nil {
			return fmt.Errorf("register auth no response")
		}
	}

	if res.StatusCode != 200 {
		// Record failed register event
		_ = p.store.AddPlatformEvent(context.Background(), storage.PlatformEventRow{
			PlatformID:   cfg.ID,
			PlatformName: cfg.Name,
			EventType:    storage.PlatformEventRegister,
			ServerIP:     cfg.ServerIP,
			ServerPort:   cfg.ServerPort,
			Details:      fmt.Sprintf("rejected: %d", res.StatusCode),
		})
		return fmt.Errorf("register rejected: %d", res.StatusCode)
	}

	p.status = true
	p.registerCallID = callID
	_ = p.store.UpdatePlatformStatus(context.Background(), cfg.ID, true)

	// Record successful register event
	_ = p.store.AddPlatformEvent(context.Background(), storage.PlatformEventRow{
		PlatformID:   cfg.ID,
		PlatformName: cfg.Name,
		EventType:    storage.PlatformEventRegister,
		ServerIP:     cfg.ServerIP,
		ServerPort:   cfg.ServerPort,
		Details:      "registered successfully",
	})

	slog.Info("[Platform] register success", "name", cfg.Name, "server", cfg.ServerGBID)
	return nil
}

// Keepalive sends a keepalive MESSAGE to the upstream platform.
func (p *Platform) Keepalive() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	cfg := p.Config
	recipient := sip.Uri{
		Scheme: "sip",
		User:   cfg.ServerGBID,
		Host:   cfg.ServerIP,
		Port:   cfg.ServerPort,
	}

	req := sip.NewRequest(sip.MESSAGE, recipient)
	req.SetTransport(cfg.Transport)

	callID := fmt.Sprintf("%s-ka-%d@%s", cfg.DeviceGBID, time.Now().Unix(), cfg.DeviceIP)
	req.AppendHeader(sip.NewHeader("Call-ID", callID))

	cseq := uint32(p.sn)
	p.sn++
	req.AppendHeader(&sip.CSeqHeader{SeqNo: cseq, MethodName: sip.MESSAGE})

	domain := cfg.DeviceGBDomain
	if domain == "" {
		domain = cfg.DeviceGBID
		if len(domain) > 10 {
			domain = domain[:10]
		}
	}
	req.AppendHeader(&sip.FromHeader{
		Address: sip.Uri{User: cfg.DeviceGBID, Host: domain},
	})
	req.AppendHeader(&sip.ToHeader{
		Address: sip.Uri{User: cfg.ServerGBID, Host: domain},
	})
	req.AppendHeader(sip.NewHeader("Content-Type", "Application/MANSCDP+xml"))
	req.AppendHeader(sip.NewHeader("Expires", fmt.Sprintf("%d", cfg.Expires)))
	maxFwd2 := sip.MaxForwardsHeader(70)
	req.AppendHeader(&maxFwd2)

	sn := p.sn
	p.sn++
	body := fmt.Sprintf(`<?xml version="1.0" encoding="GB2312"?>
<Notify>
<CmdType>Keepalive</CmdType>
<SN>%d</SN>
<DeviceID>%s</DeviceID>
</Notify>`, sn, cfg.DeviceGBID)
	req.SetBody([]byte(body))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := p.client.TransactionRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("keepalive send failed: %w", err)
	}
	defer tx.Terminate()

	res := <-tx.Responses()
	if res == nil {
		return fmt.Errorf("keepalive no response")
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("keepalive rejected: %d", res.StatusCode)
	}
	return nil
}

// OnMessage handles incoming SIP MESSAGE from upstream platform.
func (p *Platform) OnMessage(req *sip.Request, tx sip.ServerTransaction) {
	body := req.Body()
	if len(body) == 0 {
		tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		return
	}

	var msg struct {
		CmdType string `xml:"CmdType"`
		SN      int    `xml:"SN"`
		DeviceID string `xml:"DeviceID"`
	}
	if err := xmlUnmarshal(body, &msg); err != nil {
		slog.Error("[Platform] message parse error", "name", p.Config.Name, "error", err)
		tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		return
	}

	tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))

	slog.Info("[Platform] message received", "name", p.Config.Name, "cmd", msg.CmdType, "sn", msg.SN)

	switch msg.CmdType {
	case "Catalog":
		p.handleCatalogResponse(req, msg.SN, msg.DeviceID)
	case "DeviceControl":
		p.handleDeviceControl(req, msg.SN, msg.DeviceID, body)
	case "RecordInfo":
		// Forward record info query downstream
		slog.Info("[Platform] RecordInfo query received", "name", p.Config.Name, "device_id", msg.DeviceID)
	case "DeviceInfo":
		p.handleDeviceInfoResponse(req, msg.SN, msg.DeviceID)
	case "DeviceStatus":
		p.handleDeviceStatusResponse(req, msg.SN, msg.DeviceID)
	default:
		slog.Debug("[Platform] unhandled message type", "name", p.Config.Name, "cmd", msg.CmdType)
	}
}

// handleCatalogResponse responds to upstream platform's Catalog query.
func (p *Platform) handleCatalogResponse(req *sip.Request, sn int, queryDeviceID string) {
	slog.Info("[Platform] handling catalog query", "name", p.Config.Name, "sn", sn, "device_id", queryDeviceID)

	// Get shared channels
	pm := p.getManager()
	if pm == nil {
		return
	}
	channels := pm.GetSharedChannels(p.Config.ID)

	cfg := p.Config
	recipient := sip.Uri{
		Scheme: "sip",
		User:   cfg.ServerGBID,
		Host:   cfg.ServerIP,
		Port:   cfg.ServerPort,
	}

	// 按行政区划/业务分组树组装带层级的 Catalog 条目
	items := p.buildCatalogItems(channels)

	// 无共享通道时回空目录
	if len(items) == 0 {
		p.sendCatalogItem(recipient, sn, cfg, nil, 0)
		return
	}

	// 每条 MESSAGE 携带一个 Item, SumNum 为总数
	for i := range items {
		p.sendCatalogItem(recipient, sn, cfg, &items[i], len(items))
		time.Sleep(50 * time.Millisecond) // rate limit
	}
}

// sendCatalogItem 发送单条 Catalog Item (item 为 nil 时发送空目录)。
func (p *Platform) sendCatalogItem(recipient sip.Uri, sn int, cfg *PlatformConfig, item *catalogItem, sumNum int) {
	req := sip.NewRequest(sip.MESSAGE, recipient)
	req.SetTransport(cfg.Transport)

	domain := cfg.DeviceGBDomain
	if domain == "" {
		domain = cfg.DeviceGBID
		if len(domain) > 10 {
			domain = domain[:10]
		}
	}
	req.AppendHeader(&sip.FromHeader{
		Address: sip.Uri{User: cfg.DeviceGBID, Host: domain},
	})
	req.AppendHeader(&sip.ToHeader{
		Address: sip.Uri{User: cfg.ServerGBID, Host: domain},
	})
	req.AppendHeader(sip.NewHeader("Content-Type", "Application/MANSCDP+xml"))

	var xmlContent string
	if item == nil {
		xmlContent = fmt.Sprintf(`<?xml version="1.0" encoding="GB2312"?>
<Response>
<CmdType>Catalog</CmdType>
<SN>%d</SN>
<DeviceID>%s</DeviceID>
<SumNum>0</SumNum>
<DeviceList Num="0">
</DeviceList>
</Response>`, sn, cfg.DeviceGBID)
	} else {
		xmlContent = fmt.Sprintf(`<?xml version="1.0" encoding="GB2312"?>
<Response>
<CmdType>Catalog</CmdType>
<SN>%d</SN>
<DeviceID>%s</DeviceID>
<SumNum>%d</SumNum>
<DeviceList Num="1">
%s
</DeviceList>
</Response>`, sn, cfg.DeviceGBID, sumNum, buildCatalogItemXML(*item))
	}

	req.SetBody([]byte(xmlContent))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := p.client.TransactionRequest(ctx, req)
	if err != nil {
		slog.Error("[Platform] send catalog failed", "name", cfg.Name, "error", err)
		return
	}
	defer tx.Terminate()

	res := <-tx.Responses()
	if res != nil && res.StatusCode == 401 {
		p.handleAuthAndRetry(req, tx, cfg, "Catalog")
	}
}

// handleDeviceControl forwards PTZ/control commands to downstream devices.
func (p *Platform) handleDeviceControl(req *sip.Request, sn int, deviceID string, body []byte) {
	slog.Info("[Platform] DeviceControl received", "name", p.Config.Name, "device_id", deviceID)
	// The body contains the control XML which should be forwarded to the actual device
	// This requires finding the device and forwarding the MESSAGE
}

// handleDeviceInfoResponse responds to DeviceInfo query from upstream.
func (p *Platform) handleDeviceInfoResponse(req *sip.Request, sn int, deviceID string) {
	slog.Info("[Platform] DeviceInfo query received", "name", p.Config.Name, "device_id", deviceID)
	cfg := p.Config

	recipient := sip.Uri{
		Scheme: "sip",
		User:   cfg.ServerGBID,
		Host:   cfg.ServerIP,
		Port:   cfg.ServerPort,
	}

	xmlContent := fmt.Sprintf(`<?xml version="1.0" encoding="GB2312"?>
<Response>
<CmdType>DeviceInfo</CmdType>
<SN>%d</SN>
<DeviceID>%s</DeviceID>
<Result>OK</Result>
<DeviceName>%s</DeviceName>
<Manufacturer>lalmax-nvr</Manufacturer>
<Model>NVR</Model>
<Firmware>1.0</Firmware>
</Response>`, sn, cfg.DeviceGBID, cfg.Name)

	p.sendMessageToUpstream(recipient, cfg, xmlContent)
}

// handleDeviceStatusResponse responds to DeviceStatus query from upstream.
func (p *Platform) handleDeviceStatusResponse(req *sip.Request, sn int, deviceID string) {
	slog.Info("[Platform] DeviceStatus query received", "name", p.Config.Name, "device_id", deviceID)
	cfg := p.Config

	recipient := sip.Uri{
		Scheme: "sip",
		User:   cfg.ServerGBID,
		Host:   cfg.ServerIP,
		Port:   cfg.ServerPort,
	}

	online := "ONLINE"
	status := "OK"
	if !p.status {
		online = "OFFLINE"
		status = "ERROR"
	}

	xmlContent := fmt.Sprintf(`<?xml version="1.0" encoding="GB2312"?>
<Response>
<CmdType>DeviceStatus</CmdType>
<SN>%d</SN>
<DeviceID>%s</DeviceID>
<Result>OK</Result>
<Online>%s</Online>
<Status>%s</Status>
<DeviceTime>%s</DeviceTime>
<Encode>ON</Encode>
<Record>OFF</Record>
</Response>`, sn, cfg.DeviceGBID, online, status, time.Now().Format("2006-01-02T15:04:05"))

	p.sendMessageToUpstream(recipient, cfg, xmlContent)
}

func (p *Platform) sendMessageToUpstream(recipient sip.Uri, cfg *PlatformConfig, xmlContent string) {
	req := sip.NewRequest(sip.MESSAGE, recipient)
	req.SetTransport(cfg.Transport)

	domain := cfg.DeviceGBDomain
	if domain == "" {
		domain = cfg.DeviceGBID
		if len(domain) > 10 {
			domain = domain[:10]
		}
	}
	req.AppendHeader(&sip.FromHeader{
		Address: sip.Uri{User: cfg.DeviceGBID, Host: domain},
	})
	req.AppendHeader(&sip.ToHeader{
		Address: sip.Uri{User: cfg.ServerGBID, Host: domain},
	})
	req.AppendHeader(sip.NewHeader("Content-Type", "Application/MANSCDP+xml"))
	req.SetBody([]byte(xmlContent))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := p.client.TransactionRequest(ctx, req)
	if err != nil {
		slog.Error("[Platform] send message failed", "name", cfg.Name, "error", err)
		return
	}
	defer tx.Terminate()

	res := <-tx.Responses()
	if res != nil && res.StatusCode == 401 {
		p.handleAuthAndRetry(req, tx, cfg, "Message")
	}
}

func (p *Platform) handleAuthAndRetry(req *sip.Request, tx sip.ClientTransaction, cfg *PlatformConfig, logTag string) {
	// Retry with auth would go here
	slog.Debug("[Platform] auth challenge received", "name", cfg.Name, "tag", logTag)
}

func (p *Platform) getManager() *PlatformManager {
	// This is a helper to get the manager - in practice the manager is passed during init
	return nil
}

func extractNonce(header string) string {
	// Extract nonce from WWW-Authenticate header
	for _, part := range splitHeader(header) {
		if len(part) > 6 && part[:6] == "nonce=" {
			val := part[6:]
			if len(val) > 2 && val[0] == '"' {
				val = val[1:]
			}
			if len(val) > 0 && val[len(val)-1] == '"' {
				val = val[:len(val)-1]
			}
			return val
		}
	}
	return ""
}

func splitHeader(header string) []string {
	var parts []string
	current := ""
	for _, c := range header {
		if c == ',' || c == ' ' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}
