package udpts

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/naza/pkg/nazalog"
	"github.com/q191201771/naza/pkg/unique"
)

const retryInterval = time.Second

var (
	ErrDuplicateStream = errors.New("udp ts pull already exists")
	ErrSessionNotFound = errors.New("udp ts pull session not found")
	sessionUK          = unique.NewSingleGenerator("UDPTSPULL")
)

type customizePubHost interface {
	AddCustomizePubSession(streamName string) (logic.ICustomizePubSessionContext, error)
	DelCustomizePubSession(logic.ICustomizePubSessionContext)
}

type Manager struct {
	host customizePubHost

	mu    sync.Mutex
	tasks map[string]*task
}

type task struct {
	id          string
	streamName  string
	url         string
	pullTimeout int
	retryNum    int

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	pub       logic.ICustomizePubSessionContext
	publisher *Publisher
}

func NewManager(host customizePubHost) *Manager {
	return &Manager{
		host:  host,
		tasks: make(map[string]*task),
	}
}

func IsUDPURL(rawUrl string) bool {
	u, err := url.Parse(rawUrl)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, "udp")
}

func (m *Manager) Start(info base.ApiCtrlStartRelayPullReq) (ret base.ApiCtrlStartRelayPullResp) {
	if !IsUDPURL(info.Url) {
		ret.ErrorCode = base.ErrorCodeStartRelayPullFail
		ret.Desp = "url scheme must be udp"
		return
	}

	streamName := resolveStreamName(info)
	if streamName == "" {
		ret.ErrorCode = base.ErrorCodeParamMissing
		ret.Desp = "stream_name is required for udp ts pull"
		return
	}

	m.mu.Lock()
	if _, ok := m.tasks[streamName]; ok {
		m.mu.Unlock()
		ret.ErrorCode = base.ErrorCodeStartRelayPullFail
		ret.Desp = ErrDuplicateStream.Error()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	t := &task{
		id:          sessionUK.GenUniqueKey(),
		streamName:  streamName,
		url:         info.Url,
		pullTimeout: info.PullTimeoutMs,
		retryNum:    info.PullRetryNum,
		ctx:         ctx,
		cancel:      cancel,
	}
	m.tasks[streamName] = t
	m.mu.Unlock()

	nazalog.Infof("[%s] start udp ts customize pub. stream=%s url=%s program=%d", t.id, streamName, info.Url, ParseProgramID(info.Url))
	go t.run(m)

	ret.ErrorCode = base.ErrorCodeSucc
	ret.Desp = base.DespSucc
	ret.Data.StreamName = streamName
	ret.Data.SessionId = t.id
	return
}

func (m *Manager) Stop(streamName string) (sessionID string, err error) {
	m.mu.Lock()
	t, ok := m.tasks[streamName]
	if ok {
		delete(m.tasks, streamName)
	}
	m.mu.Unlock()
	if !ok {
		return "", ErrSessionNotFound
	}
	nazalog.Infof("[%s] stop udp ts customize pub. stream=%s", t.id, streamName)
	t.stop()
	return t.id, nil
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	tasks := make([]*task, 0, len(m.tasks))
	for name, t := range m.tasks {
		delete(m.tasks, name)
		tasks = append(tasks, t)
	}
	m.mu.Unlock()
	for _, t := range tasks {
		t.stop()
	}
}

func (t *task) run(m *Manager) {
	defer func() {
		t.teardown(m.host)
		m.mu.Lock()
		if cur, ok := m.tasks[t.streamName]; ok && cur == t {
			delete(m.tasks, t.streamName)
		}
		m.mu.Unlock()
	}()

	startCount := 0
	for {
		if t.ctx.Err() != nil {
			return
		}
		startCount++
		err := t.pullOnce(m.host)
		if t.ctx.Err() != nil {
			return
		}
		nazalog.Warnf("[%s] udp ts pull stopped. stream=%s err=%v", t.id, t.streamName, err)
		if t.retryNum >= 0 && startCount > t.retryNum {
			return
		}
		select {
		case <-t.ctx.Done():
			return
		case <-time.After(retryInterval):
		}
	}
}

func (t *task) pullOnce(host customizePubHost) error {
	u, err := url.Parse(t.url)
	if err != nil {
		return fmt.Errorf("parse url failed: %w", err)
	}
	remoteAddr, err := net.ResolveUDPAddr("udp", u.Host)
	if err != nil {
		return fmt.Errorf("resolve udp addr failed: %w", err)
	}
	iface := u.Query().Get("interface")

	conn, err := createPacketConn(remoteAddr, iface)
	if err != nil {
		return fmt.Errorf("listen udp failed: %w", err)
	}

	pub, err := host.AddCustomizePubSession(t.streamName)
	if err != nil {
		_ = conn.Close()
		return err
	}
	pub.WithOption(func(option *base.AvPacketStreamOption) {
		option.VideoFormat = base.AvPacketStreamVideoFormatAnnexb
	})

	publisher := NewPublisher(t.ctx, conn, t.streamName, t.pullTimeout, ParseProgramID(t.url))
	publisher.SetSession(pub)

	t.mu.Lock()
	t.pub = pub
	t.publisher = publisher
	t.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- publisher.Run()
	}()

	select {
	case err = <-done:
		t.teardown(host)
		return err
	case <-t.ctx.Done():
		t.teardown(host)
		return t.ctx.Err()
	}
}

func (t *task) stop() {
	t.cancel()
	t.mu.Lock()
	publisher := t.publisher
	t.mu.Unlock()
	if publisher != nil {
		publisher.Close()
	}
}

func (t *task) teardown(host customizePubHost) {
	t.mu.Lock()
	publisher := t.publisher
	pub := t.pub
	t.publisher = nil
	t.pub = nil
	t.mu.Unlock()

	if publisher != nil {
		publisher.Close()
	}
	if pub != nil {
		host.DelCustomizePubSession(pub)
	}
}

func resolveStreamName(info base.ApiCtrlStartRelayPullReq) string {
	if info.StreamName != "" {
		return info.StreamName
	}
	ctx, err := base.ParseUrl(info.Url, -1)
	if err == nil && ctx.LastItemOfPath != "" {
		return ctx.LastItemOfPath
	}
	u, err := url.Parse(info.Url)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ReplaceAll(u.Host, ":", "_")
}
