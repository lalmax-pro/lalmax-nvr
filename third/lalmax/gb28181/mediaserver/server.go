package mediaserver

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/naza/pkg/nazalog"
)

const (
	defaultReadTimeout   = 10 * time.Second
	udpSourceIdleTimeout = 3 * time.Second
)

type IGbObserver interface {
	CheckSsrc(ssrc uint32) (*MediaInfo, bool)
	GetMediaInfoByKey(key string) (*MediaInfo, bool)
	NotifyClose(streamName string)
	OnRtpPacket(streamName string, mediaKey string)
}

type GB28181MediaServer struct {
	listenPort int
	lalServer  logic.ILalServer

	listener net.Listener

	disposeOnce          sync.Once
	disposed             atomic.Bool
	observer             IGbObserver
	mediaKey             string
	preferMediaKeyLookup bool
	readTimeout          time.Duration

	conns       sync.Map   // keyed by *Conn and stream name
	claimMu     sync.Mutex // serialize publisher handover for a media server
	connWG      sync.WaitGroup
	acceptDone  chan struct{}
	disposeDone chan struct{}
}

func NewGB28181MediaServer(listenPort int, mediaKey string, observer IGbObserver, lal logic.ILalServer) *GB28181MediaServer {
	return &GB28181MediaServer{
		listenPort:  listenPort,
		lalServer:   lal,
		observer:    observer,
		mediaKey:    mediaKey,
		readTimeout: defaultReadTimeout,
		acceptDone:  make(chan struct{}),
		disposeDone: make(chan struct{}),
	}
}

func (s *GB28181MediaServer) WithPreferMediaKeyLookup(prefer bool) *GB28181MediaServer {
	s.preferMediaKeyLookup = prefer
	return s
}

func (s *GB28181MediaServer) WithReadTimeout(timeout time.Duration) *GB28181MediaServer {
	s.readTimeout = timeout
	return s
}

func (s *GB28181MediaServer) GetListenerPort() uint16 {
	return uint16(s.listenPort)
}
func (s *GB28181MediaServer) Start(listener net.Listener) (err error) {
	s.listener = listener
	if listener != nil {
		go func(listener net.Listener) {
			defer close(s.acceptDone)
			for {
				if s.disposed.Load() {
					return
				}
				conn, err := listener.Accept()
				if err != nil {
					var ne net.Error
					if ok := errors.As(err, &ne); ok && ne.Timeout() {
						nazalog.Error("Accept failed: timeout error, retrying...")
						time.Sleep(time.Second / 20)
						continue
					} else {
						break
					}
				}
				if conn == nil {
					continue
				}
				if s.disposed.Load() {
					conn.Close()
					return
				}

				c := NewConn(conn, s.observer, s.lalServer)
				c.SetKey(s.mediaKey)
				c.SetMediaServer(s)
				c.SetPreferMediaKeyLookup(s.preferMediaKeyLookup)
				c.SetReadTimeout(s.readTimeout)
				s.conns.Store(c, c)
				s.connWG.Add(1)
				go func() {
					defer s.connWG.Done()
					c.Serve()
					s.conns.Delete(c)
					if c.streamName != "" {
						// A former connection must never delete its successor.
						s.conns.CompareAndDelete(c.streamName, c)
					}
				}()
			}
		}(listener)
	} else {
		close(s.acceptDone)
	}
	return
}

// claimConn elects one source per stream. A competing UDP endpoint must not
// evict a publisher that is still receiving packets. The caller keeps reading
// and discarding contender packets, preventing a virtual UDP connection churn.
// Once the owner has been idle for a bounded interval the contender may take
// over; a TCP successor retains the original close-and-wait semantics.
func (s *GB28181MediaServer) claimConn(c *Conn) bool {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	if s.disposed.Load() {
		return false
	}
	if oldValue, ok := s.conns.Load(c.streamName); ok {
		old := oldValue.(*Conn)
		if old == c {
			return true
		}
		if old.conn.RemoteAddr().Network() == "udp" && c.conn.RemoteAddr().Network() == "udp" {
			last := old.lastPacket.Load()
			if last != 0 && time.Since(time.Unix(0, last)) < udpSourceIdleTimeout {
				return false
			}
		}
		old.Close()
		<-old.done // Publisher cleanup is complete before a new owner is installed.
	}
	c.lastPacket.Store(time.Now().UnixNano())
	s.conns.Store(c.streamName, c)
	return true
}

func (s *GB28181MediaServer) CloseConn(streamName string) {
	if v, ok := s.conns.Load(streamName); ok {
		conn := v.(*Conn)
		conn.Close()
		<-conn.done // Do not report receiver stopped before publisher cleanup.
	}
}

// Dispose returns only after every accepted RTP connection has executed
// its Serve deferred Publisher cleanup. This is the stop/start barrier.
func (s *GB28181MediaServer) Dispose() {
	s.disposeOnce.Do(func() {
		s.disposed.Store(true)
		if s.listener != nil {
			_ = s.listener.Close()
		}
		// No further connWG.Add occurs after the accept goroutine exits.
		<-s.acceptDone
		s.conns.Range(func(_, value any) bool { value.(*Conn).Close(); return true })
		s.connWG.Wait()
		close(s.disposeDone)
	})
	<-s.disposeDone
}
