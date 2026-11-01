package relay

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/rtmp"
)

var logger = slog.Default().With("component", "relay")

const (
	// statsSampleInterval is how often running tasks are sampled.
	statsSampleInterval = 5 * time.Second
	// statsRingCap is the per-task ring buffer size: 1 hour at 5s intervals.
	statsRingCap = 720
	// statsRingTTL is how long an idle task's history is retained before eviction.
	statsRingTTL = 1 * time.Hour
)

// statsRing is a fixed-capacity ring buffer of StatSample for one task.
type statsRing struct {
	samples  [statsRingCap]StatSample
	head     int
	count    int
	lastSeen int64
}

func (r *statsRing) append(s StatSample) {
	r.samples[r.head] = s
	r.head = (r.head + 1) % statsRingCap
	if r.count < statsRingCap {
		r.count++
	}
	r.lastSeen = s.Timestamp.Unix()
}

func (r *statsRing) since(cutoff time.Time) []StatSample {
	if r.count == 0 {
		return []StatSample{}
	}
	start := (r.head - r.count + statsRingCap) % statsRingCap
	result := make([]StatSample, 0, r.count)
	for i := 0; i < r.count; i++ {
		pos := (start + i) % statsRingCap
		if r.samples[pos].Timestamp.After(cutoff) || r.samples[pos].Timestamp.Equal(cutoff) {
			result = append(result, r.samples[pos])
		}
	}
	return result
}

// TaskStatus represents the status of a relay task.
type TaskStatus string

const (
	TaskStatusRunning TaskStatus = "running"
	TaskStatusStopped TaskStatus = "stopped"
	TaskStatusError   TaskStatus = "error"
)

// Task represents a relay push task.
type Task struct {
	ID        string     `json:"id"`
	StreamID  string     `json:"stream_id"`
	TranscodeJobID string `json:"transcode_job_id,omitempty"`
	TargetURL string     `json:"target_url"`
	Status    TaskStatus `json:"status"`
	ErrorMsg  string     `json:"error_msg,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	StoppedAt *time.Time `json:"stopped_at,omitempty"`
}

// TaskStats represents relay push statistics.
type TaskStats struct {
	TaskID       string        `json:"task_id"`
	StreamID     string        `json:"stream_id"`
	TranscodeJobID string      `json:"transcode_job_id,omitempty"`
	TargetURL    string        `json:"target_url"`
	Status       TaskStatus    `json:"status"`
	StartedAt    *time.Time    `json:"started_at,omitempty"`
	StoppedAt    *time.Time    `json:"stopped_at,omitempty"`
	VideoFPS     float64       `json:"video_fps"`
	VideoBitrate int           `json:"video_bitrate"`
	AudioFPS     float64       `json:"audio_fps"`
	AudioBitrate int           `json:"audio_bitrate"`
	OutputBitrate int          `json:"output_bitrate"`
	VideoPayloadBitrate int    `json:"video_payload_bitrate"`
	AudioPayloadBitrate int   `json:"audio_payload_bitrate"`
	BitrateScope string        `json:"bitrate_scope"`
	TotalBytes   int64         `json:"total_bytes"`
	Duration     time.Duration `json:"duration"`
}

// StatSample represents a single statistics sample.
type StatSample struct {
	Timestamp    time.Time `json:"timestamp"`
	VideoFPS     float64   `json:"video_fps"`
	VideoBitrate int       `json:"video_bitrate"`
	AudioFPS     float64   `json:"audio_fps"`
	AudioBitrate int       `json:"audio_bitrate"`
	OutputBitrate int      `json:"output_bitrate"`
	VideoPayloadBitrate int `json:"video_payload_bitrate"`
	AudioPayloadBitrate int `json:"audio_payload_bitrate"`
	BitrateScope string    `json:"bitrate_scope"`
	TotalBytes   int64     `json:"total_bytes"`
}

// StatsHistory represents historical statistics for a task.
type StatsHistory struct {
	TaskID  string       `json:"task_id"`
	Samples []StatSample `json:"samples"`
}

// Manager manages relay push tasks.
type Manager struct {
	db     storage.RelayTaskRepository
	engine media.Engine
	derivedResolver func(context.Context, string, string) (string, string, error)
	isInternalStream func(string) bool
	playbackTokenResolver func(string) string

	mu       sync.RWMutex
	tasks    map[string]*activeTask
	rings    map[string]*statsRing // in-memory ring buffers per task
	stopOnce sync.Once
	stopCh   chan struct{}
}

type activeTask struct {
	physicalStreamID string
	task        *Task
	session     *rtmp.PushSession
	pullSession *rtmp.PullSession
	cancel      context.CancelFunc
	done        chan struct{}
	statsMu     sync.Mutex
	// The RTMP push session's UpdateStat panics before Push has established
	// its connection. Both flags are protected by statsMu, including disposal.
	pushReady   bool
	disposed    bool
	videoFrames uint64
	audioFrames uint64
	videoBytes  uint64
	audioBytes uint64
	lastStatsAt time.Time
	lastVideoFrames uint64
	lastAudioFrames uint64
	lastVideoBytes uint64
	lastAudioBytes uint64
	latestStats StatSample
	hasStats    bool
}

// NewManager creates a new relay manager.
func NewManager(db storage.RelayTaskRepository, engine media.Engine) *Manager {
	m := &Manager{
		db:     db,
		engine: engine,
		tasks:  make(map[string]*activeTask),
		rings:  make(map[string]*statsRing),
		stopCh: make(chan struct{}),
	}
	// Start background stats collector
	go m.collectStatsLoop()
	return m
}

// Stop stops the manager and its background goroutines.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
	m.mu.RLock()
	active := make([]*activeTask, 0, len(m.tasks))
	for _, task := range m.tasks {
		if task.task.Status == TaskStatusRunning {
			active = append(active, task)
		}
	}
	m.mu.RUnlock()
	for _, task := range active {
		m.stopActiveTask(task)
	}
	for _, task := range active {
		if task.done == nil {
			continue
		}
		select {
		case <-task.done:
		case <-time.After(6 * time.Second):
			logger.Warn("relay task did not stop before shutdown timeout", "task_id", task.task.ID)
		}
	}
}

func (m *Manager) stopActiveTask(task *activeTask) {
	if task == nil {
		return
	}
	// Capture the last wire counters before disposing either RTMP session.
	// The snapshot remains available after an error/stop for API consumers.
	if task.task != nil {
		m.collectTaskStats(context.Background(), task.task, task)
	}
	if task.cancel != nil {
		task.cancel()
	}
	task.statsMu.Lock()
	defer task.statsMu.Unlock()
	task.pushReady = false
	task.disposed = true
	if task.pullSession != nil {
		_ = task.pullSession.Dispose()
	}
	if task.session != nil {
		_ = task.session.Dispose()
	}
}

// collectStatsLoop periodically collects stats for running tasks.
func (m *Manager) collectStatsLoop() {
	ticker := time.NewTicker(statsSampleInterval)
	defer ticker.Stop()

	cleanupTicker := time.NewTicker(5 * time.Minute)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ticker.C:
			m.collectAllStats()
		case <-cleanupTicker.C:
			m.evictIdleRings()
		case <-m.stopCh:
			return
		}
	}
}

// evictIdleRings removes ring buffers for tasks that haven't been seen recently.
func (m *Manager) evictIdleRings() {
	cutoff := time.Now().Add(-statsRingTTL).Unix()
	m.mu.Lock()
	defer m.mu.Unlock()

	for taskID, ring := range m.rings {
		if ring.lastSeen < cutoff {
			delete(m.rings, taskID)
		}
	}
}

// collectAllStats collects stats for all running tasks.
func (m *Manager) collectAllStats() {
	m.mu.RLock()
	runningTasks := make([]*activeTask, 0)
	for _, at := range m.tasks {
		if at.task.Status == TaskStatusRunning {
			runningTasks = append(runningTasks, at)
		}
	}
	m.mu.RUnlock()

	ctx := context.Background()
	for _, at := range runningTasks {
		m.collectTaskStats(ctx, at.task, at)
	}
}

// collectTaskStats collects and stores stats for a single task.
func (m *Manager) collectTaskStats(ctx context.Context, task *Task, at *activeTask) {
	if task == nil || at == nil {
		return
	}
	now := time.Now()
	sample := StatSample{
		Timestamp: now,
		BitrateScope: "output_wire;input_media_payload_components",
	}

	// Count media messages actually received by the relay. The embedded
	// engine's in_frame_per_sec endpoint is not populated for every RTMP
	// publisher, so it cannot be the sole source of relay frame-rate metrics.
	at.statsMu.Lock()
	interval := now.Sub(at.lastStatsAt)
	if at.lastStatsAt.IsZero() {
		interval = statsSampleInterval
	}
	if interval <= 0 {
		interval = time.Second
	}
	intervalSec := uint64(interval) / uint64(time.Second)
	if intervalSec == 0 {
		intervalSec = 1
	}
	videoFrames := at.videoFrames - at.lastVideoFrames
	audioFrames := at.audioFrames - at.lastAudioFrames
	videoBytes := at.videoBytes - at.lastVideoBytes
	audioBytes := at.audioBytes - at.lastAudioBytes
	seconds := interval.Seconds()
	if seconds <= 0 {
		seconds = 1
	}
	sample.VideoFPS = float64(videoFrames) / seconds
	sample.AudioFPS = float64(audioFrames) / seconds
	sample.VideoPayloadBitrate = int(float64(videoBytes*8) / seconds)
	sample.AudioPayloadBitrate = int(float64(audioBytes*8) / seconds)
	at.lastVideoFrames = at.videoFrames
	at.lastAudioFrames = at.audioFrames
	at.lastVideoBytes = at.videoBytes
	at.lastAudioBytes = at.audioBytes
	at.lastStatsAt = now

	// UpdateStat is required by lal before GetStat computes WriteBitrateKbits.
	// Serialize both calls with API reads because lal's stat object mutates its
	// cached rate fields and is not safe for concurrent UpdateStat/GetStat.
	if at.session != nil && at.pushReady {
		at.session.UpdateStat(uint32(intervalSec))
		stat := at.session.GetStat()
		sample.OutputBitrate = stat.WriteBitrateKbits * 1000
		// Preserve the existing API field's historical meaning while exposing
		// explicit output_bitrate and media-payload component rates.
		sample.VideoBitrate = sample.OutputBitrate
		sample.AudioBitrate = 0 // separate output audio bitrate is unavailable
		sample.TotalBytes = int64(stat.WroteBytesSum)
	}
	at.latestStats = sample
	at.hasStats = true
	at.statsMu.Unlock()

	// Store in memory ring buffer only (no database persistence)
	m.mu.Lock()
	ring := m.rings[task.ID]
	if ring == nil {
		ring = &statsRing{}
		m.rings[task.ID] = ring
	}
	ring.append(sample)
	m.mu.Unlock()
}

// trackRelayMedia records media actually received by the relay.
func trackRelayMedia(at *activeTask, msg base.RtmpMsg) {
	if at == nil || len(msg.Payload) == 0 {
		return
	}
	at.statsMu.Lock()
	defer at.statsMu.Unlock()
	switch msg.Header.MsgTypeId {
	case base.RtmpTypeIdVideo:
		if len(msg.Payload) >= 2 && msg.Payload[1] == base.RtmpAvcPacketTypeSeqHeader {
			return
		}
		at.videoFrames++
		at.videoBytes += uint64(len(msg.Payload))
	case base.RtmpTypeIdAudio:
		if msg.AudioCodecId() == base.RtmpSoundFormatAac && len(msg.Payload) >= 2 && msg.Payload[1] == base.RtmpAacPacketTypeSeqHeader {
			return
		}
		at.audioFrames++
		at.audioBytes += uint64(len(msg.Payload))
	}
}

// SetDerivedResolver installs a trusted source-bound, playback-token-aware
// resolver. Only the logical source and job ID are persisted.
func (m *Manager) SetDerivedResolver(
    resolve func(context.Context, string, string) (string, string, error),
    isInternal func(string) bool,
) {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.derivedResolver = resolve
    m.isInternalStream = isInternal
}

// SetPlaybackTokenResolver installs the trusted source playback-token lookup
// used by ordinary relay pulls. The token is passed only to the media engine;
// it is never persisted or logged by the relay manager.
func (m *Manager) SetPlaybackTokenResolver(resolve func(string) string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.playbackTokenResolver = resolve
}

type relayPlayURLBuilder interface {
	BuildPlayURL(context.Context, media.PlayURLRequest) (*media.PlayURL, error)
}

func buildRelaySourcePlayURL(ctx context.Context, engine relayPlayURLBuilder, streamID, token string) (string, error) {
	playURL, err := engine.BuildPlayURL(ctx, media.PlayURLRequest{
		StreamID: streamID,
		Protocol: "rtmp",
		Token:    token,
	})
	if err != nil || playURL == nil || playURL.URL == "" {
		return "", fmt.Errorf("build play URL failed")
	}
	return playURL.URL, nil
}

// CreateDerivedTask binds relay to a logical transcode job, not an ephemeral
// internal stream ID. It never creates a new transcoding encoder.
func (m *Manager) CreateDerivedTask(ctx context.Context, sourceID, jobID, targetURL string) (*Task,error) {
    if sourceID == "" || jobID == "" || targetURL == "" { return nil, fmt.Errorf("source, transcode job and target URL are required") }
    m.mu.RLock()
    resolver := m.derivedResolver
    m.mu.RUnlock()
    if resolver == nil { return nil, fmt.Errorf("derived relay is unavailable") }
    if _, _, err := resolver(ctx, sourceID, jobID); err != nil { return nil, fmt.Errorf("derived output is not playable") }
    task := &Task{ID:generateTaskID(),StreamID:sourceID,TranscodeJobID:jobID,TargetURL:targetURL,Status:TaskStatusStopped,CreatedAt:time.Now()}
    if err:=m.saveTask(task);err!=nil { return nil,fmt.Errorf("save derived relay: %w",err) }
    return task,nil
}

// CreateTask creates a new relay push task.
func (m *Manager) CreateTask(ctx context.Context, streamID, targetURL string) (*Task, error) {
    m.mu.RLock()
    internal := m.isInternalStream
    m.mu.RUnlock()
    if internal != nil && internal(streamID) {
        return nil, fmt.Errorf("derived stream requires a source-bound transcode job")
    }
	// Validate stream exists
	streams, err := m.engine.ListStreams(ctx)
	if err != nil {
		return nil, fmt.Errorf("list streams: %w", err)
	}

	found := false
	for _, s := range streams {
		if s.StreamID == streamID {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("stream %s not found", streamID)
	}

	// Create task
	task := &Task{
		ID:        generateTaskID(),
		StreamID:  streamID,
		TargetURL: targetURL,
		Status:    TaskStatusStopped,
		CreatedAt: time.Now(),
	}

	// Save to database
	if err := m.saveTask(task); err != nil {
		return nil, fmt.Errorf("save task: %w", err)
	}

	return task, nil
}

// GetTask returns a relay push task by ID.
func (m *Manager) GetTask(taskID string) (*Task, error) {
	m.mu.RLock()
	if at, ok := m.tasks[taskID]; ok {
		m.mu.RUnlock()
		return at.task, nil
	}
	m.mu.RUnlock()

	// Load from database
	return m.loadTask(taskID)
}

// ListTasks returns all relay push tasks.
func (m *Manager) ListTasks() ([]*Task, error) {
	m.mu.RLock()
	tasks := make([]*Task, 0, len(m.tasks))
	for _, at := range m.tasks {
		tasks = append(tasks, at.task)
	}
	m.mu.RUnlock()

	// Also load from database
	dbTasks, err := m.loadAllTasks()
	if err != nil {
		return nil, err
	}

	// Merge, avoiding duplicates
	seen := make(map[string]bool)
	for _, t := range tasks {
		seen[t.ID] = true
	}
	for _, t := range dbTasks {
		if !seen[t.ID] {
			tasks = append(tasks, t)
		}
	}

	return tasks, nil
}

// DeleteTask deletes a relay push task.
func (m *Manager) DeleteTask(taskID string) error {
	// Stop task if running
	m.StopTask(taskID)

	// Clean up in-memory state after the worker has stopped.
	m.mu.Lock()
	delete(m.tasks, taskID)
	delete(m.rings, taskID)
	m.mu.Unlock()

	// Delete from database
	return m.deleteTask(taskID)
}

// StartTask starts a relay push task.
func (m *Manager) StartTask(ctx context.Context, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if already running
	if at, ok := m.tasks[taskID]; ok {
		if at.task.Status == TaskStatusRunning {
			return fmt.Errorf("task %s is already running", taskID)
		}
		if at.done != nil {
			select {
			case <-at.done:
			default:
				return fmt.Errorf("task %s is still stopping", taskID)
			}
		}
	}

	// Load task
	task, err := m.loadTask(taskID)
	if err != nil {
		return err
	}

	// Resolve derived outputs at the point of use. Never persist ephemeral
	// physical stream IDs or scoped playback credentials in relay records.
	physicalID := task.StreamID
	var pullAddress string
	if task.TranscodeJobID != "" {
		if m.derivedResolver == nil { return fmt.Errorf("derived relay is not configured") }
		resolved, url, resolveErr := m.derivedResolver(ctx, task.StreamID, task.TranscodeJobID)
		if resolveErr != nil || resolved == "" || url == "" { return fmt.Errorf("derived output is not ready") }
		physicalID, pullAddress = resolved, url
	} else if m.isInternalStream != nil && m.isInternalStream(task.StreamID) {
		return fmt.Errorf("internal stream requires a source-bound transcode job")
	}
	// Check source state before requesting a playback URL. This distinguishes
	// a missing/offline source from an RTMP URL/token-resolution failure.
	streamInfo, err := m.engine.GetStream(ctx, physicalID)
	if err != nil {
		return fmt.Errorf("get stream: %w", err)
	}
	if streamInfo == nil || !streamInfo.Active {
		return fmt.Errorf("relay source is not active")
	}
	if task.TranscodeJobID == "" {
		token := ""
		if m.playbackTokenResolver != nil {
			token = m.playbackTokenResolver(task.StreamID)
		}
		pullAddress, err = buildRelaySourcePlayURL(ctx, m.engine, task.StreamID, token)
		if err != nil {
			return err
		}
	}

	// Create context for this task
	taskCtx, cancel := context.WithCancel(context.Background())

	// Create push session
	pushSession := rtmp.NewPushSession(func(option *rtmp.PushSessionOption) {
		option.PushTimeoutMs = 5000
		option.WriteAvTimeoutMs = 5000
	})
	pullSession := rtmp.NewPullSession(func(option *rtmp.PullSessionOption) {
		option.PullTimeoutMs = 5000
	})
	var at *activeTask
	pullSession.WithOnReadRtmpAvMsg(func(msg base.RtmpMsg) {
		trackRelayMedia(at, msg)
		if err := pushSession.WriteMsg(msg); err != nil {
			logger.Error("relay push write error", "task_id", taskID, "error", err)
		}
	})

	// Publish session handles before starting the worker so stop/shutdown can
	// dispose both sockets even while an RTMP handshake is still in progress.
	task.Status = TaskStatusRunning
	now := time.Now()
	task.StartedAt = &now
	at = &activeTask{
		physicalStreamID: physicalID,
		task:        task,
		session:     pushSession,
		pullSession: pullSession,
		cancel:      cancel,
		done:        make(chan struct{}),
	}
	m.tasks[taskID] = at

	// Start relay in goroutine
	go func() {
		defer close(at.done)
		defer func() {
			at.statsMu.Lock()
			at.pushReady = false
			at.disposed = true
			_ = pullSession.Dispose()
			_ = pushSession.Dispose()
			at.statsMu.Unlock()
			cancel()
		}()

		// Do not log either URL: both source bearer and destination may
		// contain credentials.
		logger.Info("starting relay push", "task_id", taskID, "stream_id", task.StreamID, "derived", task.TranscodeJobID != "")
		if taskCtx.Err() != nil {
			m.updateTaskStatus(taskID, TaskStatusStopped, "")
			stoppedAt := time.Now()
			m.updateTaskStoppedAt(taskID, &stoppedAt)
			return
		}

		// First, start push session
		err := pushSession.Push(task.TargetURL)
		if err != nil {
			if taskCtx.Err() != nil {
				m.updateTaskStatus(taskID, TaskStatusStopped, "")
				return
			}
			logger.Error("relay push failed to connect", "task_id", taskID, "code", "relay_push_failed")
			m.updateTaskStatus(taskID, TaskStatusError, "relay target connection failed")
			stoppedAt := time.Now()
			m.updateTaskStoppedAt(taskID, &stoppedAt)
			return
		}
		// UpdateStat/GetStat must not touch an unconnected or disposed session.
		// StopTask may cancel and dispose while Push is completing.
		at.statsMu.Lock()
		if !at.disposed && taskCtx.Err() == nil {
			at.pushReady = true
		}
		at.statsMu.Unlock()
		if taskCtx.Err() != nil {
			m.updateTaskStatus(taskID, TaskStatusStopped, "")
			stoppedAt := time.Now()
			m.updateTaskStoppedAt(taskID, &stoppedAt)
			return
		}

		// Start pulling
		err = pullSession.Pull(pullAddress)
		if err != nil {
			if taskCtx.Err() != nil {
				m.updateTaskStatus(taskID, TaskStatusStopped, "")
				return
			}
			logger.Error("relay pull failed", "task_id", taskID, "code", "relay_source_failed")
			m.updateTaskStatus(taskID, TaskStatusError, "relay source pull failed")
			stoppedAt := time.Now()
			m.updateTaskStoppedAt(taskID, &stoppedAt)
			return
		}

		m.updateTaskStatus(taskID, TaskStatusRunning, "")
		now := time.Now()
		m.updateTaskStartedAt(taskID, &now)

		// Wait for completion
		select {
		case err = <-pullSession.WaitChan():
			if err != nil {
				logger.Error("relay pull ended with error", "task_id", taskID, "code", "relay_source_closed")
				m.updateTaskStatus(taskID, TaskStatusError, "relay source closed unexpectedly")
			} else {
				logger.Info("relay pull ended", "task_id", taskID)
				m.updateTaskStatus(taskID, TaskStatusStopped, "")
			}
			now := time.Now()
			m.updateTaskStoppedAt(taskID, &now)
		case err = <-pushSession.WaitChan():
			if err != nil {
				logger.Error("relay push ended with error", "task_id", taskID, "code", "relay_target_closed")
				m.updateTaskStatus(taskID, TaskStatusError, "relay target closed unexpectedly")
			} else {
				logger.Info("relay push ended", "task_id", taskID)
				m.updateTaskStatus(taskID, TaskStatusStopped, "")
			}
			now := time.Now()
			m.updateTaskStoppedAt(taskID, &now)
		case <-taskCtx.Done():
			logger.Info("relay push cancelled", "task_id", taskID)
			m.updateTaskStatus(taskID, TaskStatusStopped, "")
			now := time.Now()
			m.updateTaskStoppedAt(taskID, &now)
		}
	}()

	return nil
}

// StopTask stops a relay push task.
func (m *Manager) StopTask(taskID string) error {
	m.mu.Lock()
	at, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("task %s not found", taskID)
	}

	if at.task.Status != TaskStatusRunning {
		m.mu.Unlock()
		return fmt.Errorf("task %s is not running", taskID)
	}
	m.mu.Unlock()

	m.stopActiveTask(at)
	if at.done != nil {
		select {
		case <-at.done:
		case <-time.After(6 * time.Second):
			return fmt.Errorf("task %s did not stop before timeout", taskID)
		}
	}

	return nil
}

// GetTaskStats returns statistics for a relay push task.
func (m *Manager) GetTaskStats(ctx context.Context, taskID string) (*TaskStats, error) {
	var task Task
	var at *activeTask
	m.mu.RLock()
	if active, ok := m.tasks[taskID]; ok {
		task = *active.task
		at = active
	}
	m.mu.RUnlock()
	if at == nil {
		loaded, err := m.loadTask(taskID)
		if err != nil {
			return nil, err
		}
		task = *loaded
	}

	if at != nil && task.Status == TaskStatusRunning {
		m.collectTaskStats(ctx, &task, at)
	}
	stats := &TaskStats{
		TaskID: task.ID, StreamID: task.StreamID, TranscodeJobID: task.TranscodeJobID,
		TargetURL: task.TargetURL, Status: task.Status, StartedAt: task.StartedAt, StoppedAt: task.StoppedAt,
	}
	if task.StartedAt != nil {
		if task.StoppedAt != nil {
			stats.Duration = task.StoppedAt.Sub(*task.StartedAt)
		} else {
			stats.Duration = time.Since(*task.StartedAt)
		}
	}
	if at != nil {
		at.statsMu.Lock()
		if at.hasStats {
			sample := at.latestStats
			stats.VideoFPS = sample.VideoFPS
			stats.VideoBitrate = sample.VideoBitrate
			stats.AudioFPS = sample.AudioFPS
			stats.AudioBitrate = sample.AudioBitrate
			stats.OutputBitrate = sample.OutputBitrate
			stats.VideoPayloadBitrate = sample.VideoPayloadBitrate
			stats.AudioPayloadBitrate = sample.AudioPayloadBitrate
			stats.BitrateScope = sample.BitrateScope
			stats.TotalBytes = sample.TotalBytes
		}
		at.statsMu.Unlock()
	}
	return stats, nil
}

// Helper methods

func (m *Manager) updateTaskStatus(taskID string, status TaskStatus, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if at, ok := m.tasks[taskID]; ok {
		at.task.Status = status
		at.task.ErrorMsg = errMsg
		m.saveTask(at.task)
	}
}

func (m *Manager) updateTaskStartedAt(taskID string, startedAt *time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if at, ok := m.tasks[taskID]; ok {
		at.task.StartedAt = startedAt
		m.saveTask(at.task)
	}
}

func (m *Manager) updateTaskStoppedAt(taskID string, stoppedAt *time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if at, ok := m.tasks[taskID]; ok {
		at.task.StoppedAt = stoppedAt
		m.saveTask(at.task)
	}
}

// Database methods

func (m *Manager) saveTask(task *Task) error {
	return m.db.SaveRelayTask(context.Background(), storage.RelayTaskRecord{
		ID:        task.ID,
		StreamID:  task.StreamID,
		TranscodeJobID: task.TranscodeJobID,
		TargetURL: task.TargetURL,
		Status:    string(task.Status),
		ErrorMsg:  task.ErrorMsg,
		CreatedAt: task.CreatedAt,
		StartedAt: task.StartedAt,
		StoppedAt: task.StoppedAt,
	})
}

func (m *Manager) loadTask(taskID string) (*Task, error) {
	row, err := m.db.GetRelayTask(context.Background(), taskID)
	if err != nil {
		return nil, fmt.Errorf("task %s not found", taskID)
	}
	return relayTaskFromRecord(row), nil
}

func (m *Manager) loadAllTasks() ([]*Task, error) {
	rows, err := m.db.ListRelayTasks(context.Background())
	if err != nil {
		return nil, err
	}
	tasks := make([]*Task, 0, len(rows))
	for i := range rows {
		tasks = append(tasks, relayTaskFromRecord(&rows[i]))
	}
	return tasks, nil
}

func (m *Manager) deleteTask(taskID string) error {
	return m.db.DeleteRelayTask(context.Background(), taskID)
}

func relayTaskFromRecord(record *storage.RelayTaskRecord) *Task {
	return &Task{
		ID:        record.ID,
		StreamID:  record.StreamID,
		TranscodeJobID: record.TranscodeJobID,
		TargetURL: record.TargetURL,
		Status:    TaskStatus(record.Status),
		ErrorMsg:  record.ErrorMsg,
		CreatedAt: record.CreatedAt,
		StartedAt: record.StartedAt,
		StoppedAt: record.StoppedAt,
	}
}

// GetTaskStatsHistory returns historical statistics for a task from memory ring buffer.
func (m *Manager) GetTaskStatsHistory(taskID string, duration time.Duration) (*StatsHistory, error) {
	// Verify task exists
	_, err := m.GetTask(taskID)
	if err != nil {
		return nil, err
	}

	since := time.Now().Add(-duration)
	history := &StatsHistory{
		TaskID:  taskID,
		Samples: make([]StatSample, 0),
	}

	// Get from the in-memory ring buffer while holding its manager lock.
	// Sampling appends and history reads must not race.
	m.mu.RLock()
	if ring := m.rings[taskID]; ring != nil {
		history.Samples = ring.since(since)
	}
	m.mu.RUnlock()

	return history, nil
}

// CleanupOldStats removes statistics older than the specified duration (no-op, stats are memory-only).
func (m *Manager) CleanupOldStats(maxAge time.Duration) error {
	return nil
}

// timeToDB converts a time pointer to a SQLite-compatible string pointer.
func timeToDB(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := t.UTC().Format("2006-01-02 15:04:05.999999999")
	return &s
}

// parseTime parses a SQLite timestamp string back into time.Time (UTC).
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	// Canonical format
	if t, err := time.Parse("2006-01-02 15:04:05.999999999", s); err == nil {
		return t, nil
	}
	// Without fractional seconds
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t, nil
	}
	// RFC3339 variants
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse time: %s", s)
}

// generateTaskID generates a unique task ID.
func generateTaskID() string {
	return fmt.Sprintf("relay_%d", time.Now().UnixNano())
}
