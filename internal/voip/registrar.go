package voip

import (
	"sync"
	"time"
)

type Registration struct {
	User       string
	Contact    string
	Expires    time.Time
	RemoteAddr string
	Transport  TransportType
	UserAgent  string
}

type Registrar struct {
	registrations map[string]*Registration
	mutex         sync.RWMutex
	closeOnce     sync.Once
	stop          chan struct{}
	done          chan struct{}
}

func NewRegistrar() *Registrar {
	r := &Registrar{
		registrations: make(map[string]*Registration),
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
	go r.runExpiry()
	return r
}

func (r *Registrar) Register(user, contact, userAgent string, expiresSeconds int) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if expiresSeconds <= 0 {
		expiresSeconds = 3600
	}

	r.registrations[user] = &Registration{
		User:      user,
		Contact:   contact,
		Expires:   time.Now().Add(time.Duration(expiresSeconds) * time.Second),
		UserAgent: userAgent,
	}
}

func (r *Registrar) Unregister(user string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	delete(r.registrations, user)
}

func (r *Registrar) Get(user string) *Registration {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	reg := r.registrations[user]
	if reg == nil || !time.Now().Before(reg.Expires) {
		return nil
	}
	copy := *reg
	return &copy
}

func (r *Registrar) IsRegistered(user string) bool {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	reg, ok := r.registrations[user]
	if !ok {
		return false
	}
	return time.Now().Before(reg.Expires)
}

func (r *Registrar) runExpiry() {
	defer close(r.done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
		}
		r.mutex.Lock()
		now := time.Now()
		for user, reg := range r.registrations {
			if now.After(reg.Expires) {
				delete(r.registrations, user)
			}
		}
		r.mutex.Unlock()
	}
}

func (r *Registrar) Close() {
	r.closeOnce.Do(func() { close(r.stop) })
	<-r.done
}

func (r *Registrar) SetPeer(user string, peer TransportAddr) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if registration := r.registrations[user]; registration != nil {
		registration.RemoteAddr = peer.Addr
		registration.Transport = peer.Type
	}
}
