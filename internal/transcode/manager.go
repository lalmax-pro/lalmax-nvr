package transcode

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"
)

// Executor resolves trusted endpoints and starts a backend; readiness is checked
// separately so a successfully forked process is never reported as live media.
type Executor interface {
	Start(context.Context, SourceRef, Profile, string) (Process, error)
	Ready(context.Context, string) bool
}
type Runtime struct {
	Key       string `json:"key"`
	VariantID string `json:"variant_id"`
	State     State  `json:"state"`
	ErrorCode string `json:"error_code,omitempty"`
}
type managedWork struct {
	runtime Runtime
	owners  map[string]bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// Manager shares effective work across jobs. Its parent context belongs to the
// NVR lifecycle, never to the HTTP request that acquires an output.
type Manager struct {
	ctx            context.Context
	executor       Executor
	limit          int
	mu             sync.Mutex
	work           map[string]*managedWork
	jobs           map[string]string
	generations    map[string]uint64
	closed         bool
	startupTimeout time.Duration
	healthTimeout  time.Duration
}

func NewManager(ctx context.Context, executor Executor, limit int) *Manager {
	if limit < 1 {
		limit = 2
	}
	return &Manager{ctx: ctx, executor: executor, limit: limit, work: map[string]*managedWork{}, jobs: map[string]string{}, generations: map[string]uint64{}, startupTimeout: 20 * time.Second, healthTimeout: 10 * time.Second}
}
func (m *Manager) Acquire(job Job, profile Profile) (Runtime, error) {
	if err := job.Validate(); err != nil {
		return Runtime{}, err
	}
	if !job.Enabled {
		return Runtime{}, &Error{Code: "job_disabled", Message: "enable the job before starting it"}
	}
	if job.ProfileID != profile.ID || job.ProfileRevision != profile.Revision {
		return Runtime{}, conflictError()
	}
	key, err := WorkKey(job.Source, profile)
	if err != nil {
		return Runtime{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return Runtime{}, &Error{Code: "service_stopped", Message: "transcoding service is stopping"}
	}
	if job.Generation == 0 || job.Generation < m.generations[job.ID] {
		return Runtime{}, conflictError()
	}
	if _, active := m.jobs[job.ID]; active && job.Generation != m.generations[job.ID] {
		return Runtime{}, conflictError()
	}
	if old, ok := m.jobs[job.ID]; ok && old != key {
		return Runtime{}, &Error{Code: "job_running", Message: "stop the old job generation before updating"}
	}
	owners := map[string]bool{job.ID: true}
	if w, ok := m.work[key]; ok {
		select {
		case <-w.done:
			// Preserve every owner's demand when restarting completed work.
			for id := range w.owners {
				owners[id] = true
			}
		default:
			if w.runtime.State == Stopping || w.runtime.State == Failed || w.runtime.State == Stopped {
				return Runtime{}, &Error{Code: "job_stopping", Message: "backend cleanup is still in progress"}
			}
			w.owners[job.ID] = true
			m.jobs[job.ID] = key
			m.generations[job.ID] = job.Generation
			return w.runtime, nil
		}
	}
	active := 0
	for _, w := range m.work {
		select {
		case <-w.done:
		default:
			active++
		}
	}
	if active >= m.limit {
		return Runtime{}, &Error{Code: "capacity_exceeded", Message: "transcoding concurrency limit reached"}
	}
	if m.executor == nil {
		return Runtime{}, &Error{Code: "backend_unavailable", Message: "media executor is not configured"}
	}
	ctx, cancel := context.WithCancel(m.ctx)
	w := &managedWork{runtime: Runtime{Key: key, VariantID: "nvr_tc_" + rand.Text(), State: Starting}, owners: owners, cancel: cancel, done: make(chan struct{})}
	m.work[key] = w
	m.jobs[job.ID] = key
	m.generations[job.ID] = job.Generation
	go m.run(ctx, w, job.Source, profile)
	return w.runtime, nil
}
func conflictError() error {
	return &Error{Code: "revision_conflict", Message: "job profile revision does not match"}
}
func (m *Manager) set(w *managedWork, state State, code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w.runtime.State == Stopping && state != Stopped {
		return
	}
	w.runtime.State = state
	w.runtime.ErrorCode = code
}
func (m *Manager) run(ctx context.Context, w *managedWork, source SourceRef, profile Profile) {
	defer close(w.done)
	// Arm the deadline before resolving endpoints or starting the backend. Stop
	// it once Start returns; media output gets the separate readiness deadline.
	startTimer := time.AfterFunc(m.startupTimeout, w.cancel)
	p, err := m.executor.Start(ctx, source, profile, w.runtime.VariantID)
	startTimer.Stop()
	if err != nil {
		code := "backend_start_failed"
		var domain *Error
		if errors.As(err, &domain) && domain.Code != "" {
			code = domain.Code
		}
		if ctx.Err() != nil {
			code = "start_canceled_or_timed_out"
		}
		m.set(w, Failed, code)
		return
	}
	exited := make(chan error, 1)
	go func() { exited <- p.Wait() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	startup := time.NewTimer(m.startupTimeout)
	defer startup.Stop()
	ready := false
	timedOut := false
	lastHealthy := time.Now()
	for {
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = p.Stop(stopCtx)
			cancel()
			<-exited
			if timedOut {
				m.set(w, Failed, "output_timeout")
			} else {
				m.set(w, Stopped, "")
			}
			return
		case err := <-exited:
			if timedOut {
				m.set(w, Failed, "output_timeout")
				return
			}
			if err != nil {
				m.set(w, Failed, "backend_exited")
			} else {
				m.set(w, Stopped, "")
			}
			return
		case <-startup.C:
			if !ready {
				timedOut = true
				m.set(w, Failed, "output_timeout")
				w.cancel()
			}
		case <-ticker.C:
			checkCtx, cancel := context.WithTimeout(ctx, time.Second)
			ok := m.executor.Ready(checkCtx, w.runtime.VariantID)
			cancel()
			if ok {
				lastHealthy = time.Now()
				if !ready {
					ready = true
					startup.Stop()
					m.set(w, Running, "")
				}
			} else if ready && time.Since(lastHealthy) >= m.healthTimeout {
				timedOut = true
				m.set(w, Failed, "output_timeout")
				w.cancel()
			}
		}
	}
}
func (m *Manager) Status(jobID string) (Runtime, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key, ok := m.jobs[jobID]
	if !ok {
		return Runtime{}, false
	}
	w, ok := m.work[key]
	if !ok {
		return Runtime{}, false
	}
	return w.runtime, true
}
func (m *Manager) Release(ctx context.Context, jobID string, generation uint64) error {
	m.mu.Lock()
	key, ok := m.jobs[jobID]
	if !ok {
		// No active lease remains. A stop/delete for a newer persisted
		// generation is valid even if it was never started; stale releases
		// cannot affect a future lease because the watermark is removed below.
		if current, known := m.generations[jobID]; known && generation < current {
			m.mu.Unlock()
			return conflictError()
		}
		delete(m.generations, jobID)
		m.mu.Unlock()
		return nil
	}
	if current, known := m.generations[jobID]; known && current != generation {
		m.mu.Unlock()
		return conflictError()
	}
	w := m.work[key]
	delete(m.jobs, jobID)
	delete(w.owners, jobID)
	if len(w.owners) > 0 {
		// This owner no longer has a lease. Its persisted intent may now be
		// deleted/recreated; other owners retain the shared physical process.
		delete(m.generations, jobID)
		m.mu.Unlock()
		return nil
	}
	w.runtime.State = Stopping
	w.cancel()
	m.mu.Unlock()
	go func() {
		<-w.done
		m.mu.Lock()
		if m.work[key] == w && len(w.owners) == 0 {
			delete(m.work, key)
		}
		m.mu.Unlock()
	}()
	select {
	case <-w.done:
		m.mu.Lock()
		if m.work[key] == w {
			delete(m.work, key)
		}
		if m.generations[jobID] == generation {
			delete(m.generations, jobID)
		}
		m.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	works := make([]*managedWork, 0, len(m.work))
	for _, w := range m.work {
		w.runtime.State = Stopping
		w.cancel()
		works = append(works, w)
	}
	m.mu.Unlock()
	for _, w := range works {
		select {
		case <-w.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.mu.Lock()
	m.work = map[string]*managedWork{}
	m.jobs = map[string]string{}
	m.mu.Unlock()
	return nil
}
