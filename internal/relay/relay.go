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
	ID         string     `json:"id"`
	StreamID   string     `json:"stream_id"`
	TargetURL  string     `json:"target_url"`
	Status     TaskStatus `json:"status"`
	ErrorMsg   string     `json:"error_msg,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	StoppedAt  *time.Time `json:"stopped_at,omitempty"`
}

// TaskStats represents relay push statistics.
type TaskStats struct {
	TaskID       string        `json:"task_id"`
	StreamID     string        `json:"stream_id"`
	TargetURL    string        `json:"target_url"`
	Status       TaskStatus    `json:"status"`
	StartedAt    *time.Time    `json:"started_at,omitempty"`
	StoppedAt    *time.Time    `json:"stopped_at,omitempty"`
	VideoFPS     float64       `json:"video_fps"`
	VideoBitrate int           `json:"video_bitrate"`
	AudioFPS     float64       `json:"audio_fps"`
	AudioBitrate int           `json:"audio_bitrate"`
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
	TotalBytes   int64     `json:"total_bytes"`
}

// StatsHistory represents historical statistics for a task.
type StatsHistory struct {
	TaskID  string       `json:"task_id"`
	Samples []StatSample `json:"samples"`
}

// Manager manages relay push tasks.
type Manager struct {
	db     *storage.DB
	engine media.Engine

	mu       sync.RWMutex
	tasks    map[string]*activeTask
	rings    map[string]*statsRing // in-memory ring buffers per task
	stopOnce sync.Once
	stopCh   chan struct{}
}

type activeTask struct {
	task    *Task
	session *rtmp.PushSession
	cancel  context.CancelFunc
}

// NewManager creates a new relay manager.
func NewManager(db *storage.DB, engine media.Engine) *Manager {
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
	now := time.Now()
	sample := StatSample{
		Timestamp: now,
	}

	// Get source stream info for FPS
	streamInfo, err := m.engine.GetStream(ctx, task.StreamID)
	if err == nil && streamInfo != nil {
		sample.VideoFPS = streamInfo.InFPS
		sample.AudioFPS = 0 // lal doesn't separate audio/video FPS
	}

	// Get push session stats for bitrate and bytes
	if at.session != nil {
		stat := at.session.GetStat()
		sample.VideoBitrate = stat.WriteBitrateKbits * 1000 // convert to bps
		sample.AudioBitrate = 0                               // lal doesn't separate audio/video bitrate
		sample.TotalBytes = int64(stat.WroteBytesSum)
	}

	// Store in memory ring buffer
	m.mu.Lock()
	ring := m.rings[task.ID]
	if ring == nil {
		ring = &statsRing{}
		m.rings[task.ID] = ring
	}
	ring.append(sample)
	m.mu.Unlock()

	// Also persist to database for long-term storage
	m.saveStatSample(task.ID, sample)
}

// CreateTask creates a new relay push task.
func (m *Manager) CreateTask(ctx context.Context, streamID, targetURL string) (*Task, error) {
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

	// Clean up memory ring buffer
	m.mu.Lock()
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
	if at, ok := m.tasks[taskID]; ok && at.task.Status == TaskStatusRunning {
		return fmt.Errorf("task %s is already running", taskID)
	}

	// Load task
	task, err := m.loadTask(taskID)
	if err != nil {
		return err
	}

	// Get stream info
	streamInfo, err := m.engine.GetStream(ctx, task.StreamID)
	if err != nil {
		return fmt.Errorf("get stream: %w", err)
	}
	if !streamInfo.Active {
		return fmt.Errorf("stream %s is not active", task.StreamID)
	}

	// Build RTMP pull URL from lalmax
	pullURL, err := m.engine.BuildPlayURL(ctx, media.PlayURLRequest{
		StreamID: task.StreamID,
		Protocol: "rtmp",
	})
	if err != nil {
		return fmt.Errorf("build play URL: %w", err)
	}

	// Create context for this task
	taskCtx, cancel := context.WithCancel(context.Background())

	// Create push session
	pushSession := rtmp.NewPushSession(func(option *rtmp.PushSessionOption) {
		option.PushTimeoutMs = 5000
		option.WriteAvTimeoutMs = 5000
	})

	// Start relay in goroutine
	go func() {
		logger.Info("starting relay push", "task_id", taskID, "stream_id", task.StreamID, 
			"source", pullURL.URL, "target", task.TargetURL)
		
		// First, start push session
		err := pushSession.Push(task.TargetURL)
		if err != nil {
			logger.Error("relay push failed to connect", "task_id", taskID, "error", err)
			m.updateTaskStatus(taskID, TaskStatusError, err.Error())
			cancel()
			return
		}

		// Create pull session with callback to forward messages
		pullSession := rtmp.NewPullSession(func(option *rtmp.PullSessionOption) {
			option.PullTimeoutMs = 5000
		})
		
		pullSession.WithOnReadRtmpAvMsg(func(msg base.RtmpMsg) {
			// Forward message to push session
			if err := pushSession.WriteMsg(msg); err != nil {
				logger.Error("relay push write error", "task_id", taskID, "error", err)
				// Don't cancel here, let the pull session handle the error
			}
		})

		// Start pulling
		err = pullSession.Pull(pullURL.URL)
		if err != nil {
			logger.Error("relay pull failed", "task_id", taskID, "error", err)
			pushSession.Dispose()
			m.updateTaskStatus(taskID, TaskStatusError, err.Error())
			cancel()
			return
		}

		m.updateTaskStatus(taskID, TaskStatusRunning, "")
		now := time.Now()
		m.updateTaskStartedAt(taskID, &now)

		// Wait for completion
		select {
		case err = <-pullSession.WaitChan():
			if err != nil {
				logger.Error("relay pull ended with error", "task_id", taskID, "error", err)
				m.updateTaskStatus(taskID, TaskStatusError, err.Error())
			} else {
				logger.Info("relay pull ended", "task_id", taskID)
				m.updateTaskStatus(taskID, TaskStatusStopped, "")
			}
			pushSession.Dispose()
			now := time.Now()
			m.updateTaskStoppedAt(taskID, &now)
		case err = <-pushSession.WaitChan():
			if err != nil {
				logger.Error("relay push ended with error", "task_id", taskID, "error", err)
				m.updateTaskStatus(taskID, TaskStatusError, err.Error())
			} else {
				logger.Info("relay push ended", "task_id", taskID)
				m.updateTaskStatus(taskID, TaskStatusStopped, "")
			}
			pullSession.Dispose()
			now := time.Now()
			m.updateTaskStoppedAt(taskID, &now)
		case <-taskCtx.Done():
			logger.Info("relay push cancelled", "task_id", taskID)
			pullSession.Dispose()
			pushSession.Dispose()
			m.updateTaskStatus(taskID, TaskStatusStopped, "")
			now := time.Now()
			m.updateTaskStoppedAt(taskID, &now)
		}
	}()

	// Store active task
	task.Status = TaskStatusRunning
	now := time.Now()
	task.StartedAt = &now
	m.tasks[taskID] = &activeTask{
		task:    task,
		session: pushSession,
		cancel:  cancel,
	}

	return nil
}

// StopTask stops a relay push task.
func (m *Manager) StopTask(taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	at, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}

	if at.task.Status != TaskStatusRunning {
		return fmt.Errorf("task %s is not running", taskID)
	}

	// Cancel the context
	at.cancel()

	// Remove from active tasks
	delete(m.tasks, taskID)

	return nil
}

// GetTaskStats returns statistics for a relay push task.
func (m *Manager) GetTaskStats(ctx context.Context, taskID string) (*TaskStats, error) {
	task, err := m.GetTask(taskID)
	if err != nil {
		return nil, err
	}

	stats := &TaskStats{
		TaskID:    task.ID,
		StreamID:  task.StreamID,
		TargetURL: task.TargetURL,
		Status:    task.Status,
		StartedAt: task.StartedAt,
		StoppedAt: task.StoppedAt,
	}

	if task.StartedAt != nil {
		if task.StoppedAt != nil {
			stats.Duration = task.StoppedAt.Sub(*task.StartedAt)
		} else {
			stats.Duration = time.Since(*task.StartedAt)
		}
	}

	// Get real-time stats from push session and stream
	if task.Status == TaskStatusRunning {
		// Get source stream info for FPS
		streamInfo, err := m.engine.GetStream(ctx, task.StreamID)
		if err == nil && streamInfo != nil {
			stats.VideoFPS = streamInfo.InFPS
		}

		// Get push session stats for bitrate and bytes
		m.mu.RLock()
		at, ok := m.tasks[taskID]
		m.mu.RUnlock()
		if ok && at.session != nil {
			stat := at.session.GetStat()
			stats.VideoBitrate = stat.WriteBitrateKbits * 1000
			stats.TotalBytes = int64(stat.WroteBytesSum)
		}
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
	query := `INSERT OR REPLACE INTO relay_tasks (id, stream_id, target_url, status, error_msg, created_at, started_at, stopped_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	
	_, err := m.db.DB().Exec(query,
		task.ID,
		task.StreamID,
		task.TargetURL,
		task.Status,
		task.ErrorMsg,
		task.CreatedAt.UTC().Format("2006-01-02 15:04:05.999999999"),
		timeToDB(task.StartedAt),
		timeToDB(task.StoppedAt),
	)
	return err
}

func (m *Manager) loadTask(taskID string) (*Task, error) {
	query := `SELECT id, stream_id, target_url, status, error_msg, created_at, started_at, stopped_at
		FROM relay_tasks WHERE id = ?`
	
	task := &Task{}
	var createdAt string
	var startedAt, stoppedAt *string
	
	err := m.db.DB().QueryRow(query, taskID).Scan(
		&task.ID,
		&task.StreamID,
		&task.TargetURL,
		&task.Status,
		&task.ErrorMsg,
		&createdAt,
		&startedAt,
		&stoppedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("task %s not found", taskID)
	}
	
	task.CreatedAt, _ = parseTime(createdAt)
	if startedAt != nil {
		t, _ := parseTime(*startedAt)
		task.StartedAt = &t
	}
	if stoppedAt != nil {
		t, _ := parseTime(*stoppedAt)
		task.StoppedAt = &t
	}
	
	return task, nil
}

func (m *Manager) loadAllTasks() ([]*Task, error) {
	query := `SELECT id, stream_id, target_url, status, error_msg, created_at, started_at, stopped_at
		FROM relay_tasks ORDER BY created_at DESC`
	
	rows, err := m.db.DB().Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	var tasks []*Task
	for rows.Next() {
		task := &Task{}
		var createdAt string
		var startedAt, stoppedAt *string
		
		err := rows.Scan(
			&task.ID,
			&task.StreamID,
			&task.TargetURL,
			&task.Status,
			&task.ErrorMsg,
			&createdAt,
			&startedAt,
			&stoppedAt,
		)
		if err != nil {
			continue
		}
		
		task.CreatedAt, _ = parseTime(createdAt)
		if startedAt != nil {
			t, _ := parseTime(*startedAt)
			task.StartedAt = &t
		}
		if stoppedAt != nil {
			t, _ := parseTime(*stoppedAt)
			task.StoppedAt = &t
		}
		
		tasks = append(tasks, task)
	}
	
	return tasks, nil
}

func (m *Manager) deleteTask(taskID string) error {
	query := `DELETE FROM relay_tasks WHERE id = ?`
	_, err := m.db.DB().Exec(query, taskID)
	return err
}

// saveStatSample saves a statistics sample to the database.
func (m *Manager) saveStatSample(taskID string, sample StatSample) error {
	query := `INSERT INTO relay_task_stats (task_id, timestamp, video_fps, video_bitrate, audio_fps, audio_bitrate, total_bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	
	_, err := m.db.DB().Exec(query,
		taskID,
		sample.Timestamp.UTC().Format("2006-01-02 15:04:05.999999999"),
		sample.VideoFPS,
		sample.VideoBitrate,
		sample.AudioFPS,
		sample.AudioBitrate,
		sample.TotalBytes,
	)
	return err
}

// GetTaskStatsHistory returns historical statistics for a task.
// It first checks the in-memory ring buffer, then falls back to database.
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

	// Try in-memory ring buffer first (faster)
	m.mu.RLock()
	ring := m.rings[taskID]
	m.mu.RUnlock()

	if ring != nil {
		samples := ring.since(since)
		if len(samples) > 0 {
			history.Samples = samples
			return history, nil
		}
	}

	// Fall back to database for historical data
	query := `SELECT timestamp, video_fps, video_bitrate, audio_fps, audio_bitrate, total_bytes
		FROM relay_task_stats 
		WHERE task_id = ? AND timestamp >= ?
		ORDER BY timestamp ASC`
	
	rows, err := m.db.DB().Query(query, taskID, since.UTC().Format("2006-01-02 15:04:05.999999999"))
	if err != nil {
		return history, nil
	}
	defer rows.Close()

	for rows.Next() {
		var sample StatSample
		var timestamp string
		
		err := rows.Scan(
			&timestamp,
			&sample.VideoFPS,
			&sample.VideoBitrate,
			&sample.AudioFPS,
			&sample.AudioBitrate,
			&sample.TotalBytes,
		)
		if err != nil {
			continue
		}
		
		sample.Timestamp, _ = parseTime(timestamp)
		history.Samples = append(history.Samples, sample)
	}

	return history, nil
}

// CleanupOldStats removes statistics older than the specified duration.
func (m *Manager) CleanupOldStats(maxAge time.Duration) error {
	cutoff := time.Now().Add(-maxAge)
	query := `DELETE FROM relay_task_stats WHERE timestamp < ?`
	_, err := m.db.DB().Exec(query, cutoff.UTC().Format("2006-01-02 15:04:05.999999999"))
	return err
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