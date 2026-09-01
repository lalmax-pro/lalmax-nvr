package merge

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

var logger = slog.Default().With("component", "merge-manager")

// MergeStatus holds the current status of the merge manager.
type MergeStatus struct {
	LastRunTime    time.Time `json:"last_run_time"`
	SegmentsMerged int       `json:"segments_merged"`
	FilesCreated   int       `json:"files_created"`
	ErrorCount     int       `json:"error_count"`
}

// MergeManager handles periodic merging of consecutive MP4 segments.
type MergeManager struct {
	mu          sync.RWMutex
	status      MergeStatus
	db          *storage.DB
	store       *storage.Manager
	getGlobalCfg func() config.MergeConfig
	getCameraCfg func(cameraID string) *config.MergeConfig
	cameras     func() []config.CameraConfig
	cameraMu    sync.Mutex
	cameraLocks map[string]*sync.Mutex
}

// NewMergeManager creates a new MergeManager with the given dependencies.
// getGlobalCfg is called on each RunOnce to support config hot-reload.
// getCameraCfg returns per-camera merge config override (nil = use global).
func NewMergeManager(
	db *storage.DB,
	store *storage.Manager,
	getGlobalCfg func() config.MergeConfig,
	getCameraCfg func(cameraID string) *config.MergeConfig,
	cameras func() []config.CameraConfig,
) *MergeManager {
	return &MergeManager{
		db:           db,
		store:        store,
		getGlobalCfg: getGlobalCfg,
		getCameraCfg: getCameraCfg,
		cameras:      cameras,
		cameraLocks:  make(map[string]*sync.Mutex),
	}
}

func (m *MergeManager) lockCamera(cameraID string) func() {
	m.cameraMu.Lock()
	lk, ok := m.cameraLocks[cameraID]
	if !ok {
		lk = &sync.Mutex{}
		m.cameraLocks[cameraID] = lk
	}
	m.cameraMu.Unlock()
	lk.Lock()
	return lk.Unlock
}

// Status returns a snapshot of the current merge status.
func (m *MergeManager) Status() MergeStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

// PendingCounts returns per-camera pending merge segment counts.
func (m *MergeManager) PendingCounts(ctx context.Context) map[string]int {
	cfg := m.getGlobalCfg()
	minAge, err := time.ParseDuration(cfg.MinSegmentAge)
	if err != nil {
		minAge = 10 * time.Minute
	}

	cameras := m.cameras()
	counts := make(map[string]int, len(cameras))
	for _, cam := range cameras {
		if !cam.Enabled {
			continue
		}
		effectiveCfg := config.ResolveMergeConfig(cfg, m.getCameraCfg(cam.ID))
		if !effectiveCfg.Enabled {
			continue
		}
		windows, err := m.db.ListCameraMergeWindows(ctx, cam.ID, minAge)
		if err != nil {
			continue
		}
		for _, w := range windows {
			counts[cam.ID] += w.SegmentCount
		}
	}
	return counts
}

func (m *MergeManager) Run(ctx context.Context) {
	cfg := m.getGlobalCfg()
	interval, err := time.ParseDuration(cfg.CheckInterval)
	if err != nil || interval <= 0 {
		interval = time.Hour
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run once immediately
	m.RunOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.RunOnce(ctx)
		}
	}
}

// RunOnce performs a single merge pass across all enabled cameras.
// It enforces a batch limit on total segments processed per run.
// Config is resolved fresh on each call for hot-reload support.
func (m *MergeManager) RunOnce(ctx context.Context) error {
	cfg := m.getGlobalCfg()

	minAge, err := time.ParseDuration(cfg.MinSegmentAge)
	if err != nil {
		minAge = 10 * time.Minute
	}

	cameras := m.cameras()
	var totalMerged int
	var totalSegments int
	var totalFreed int64
	var totalErrors int
	var processedSegments int

	for _, cam := range cameras {
		if !cam.Enabled {
			continue
		}
		if ctx.Err() != nil {
			break
		}

		// Resolve per-camera config via hot-reload callbacks.
		effectiveCfg := config.ResolveMergeConfig(cfg, m.getCameraCfg(cam.ID))
		if !effectiveCfg.Enabled {
			continue
		}

		merged, segments, freed, mergeErr := m.processCamera(ctx, cam.ID, minAge, effectiveCfg)
		if mergeErr != nil {
			logger.Error("merge pass error for camera", "camera_id", cam.ID, "error", mergeErr)
			totalErrors++
			continue
		}
		totalMerged += merged
		totalSegments += segments
		totalFreed += freed
		processedSegments += segments
		if processedSegments >= effectiveCfg.BatchLimit {
			logger.Info("batch limit reached, stopping merge pass", "limit", effectiveCfg.BatchLimit)
			break
		}
	}

	if totalMerged > 0 {
		logger.Info("merge pass complete",
			"merged_groups", totalMerged,
			"merged_segments", totalSegments,
			"freed_bytes", totalFreed,
		)
	}

	// Update status under lock.
	m.mu.Lock()
	m.status.LastRunTime = time.Now()
	m.status.SegmentsMerged = totalSegments
	m.status.FilesCreated = totalMerged
	m.status.ErrorCount = totalErrors
	m.mu.Unlock()

	return nil
}

// MergeCamera performs a single merge pass for the given camera.
// It resolves the effective merge config (global + per-camera override) and delegates to processCamera.
// Errors are logged but never returned — the method is intentionally non-blocking for the archive flow.
func (m *MergeManager) MergeCamera(ctx context.Context, cameraID string) error {
	cfg := m.getGlobalCfg()

	minAge, err := time.ParseDuration(cfg.MinSegmentAge)
	if err != nil {
		minAge = 10 * time.Minute
	}

	effectiveCfg := config.ResolveMergeConfig(cfg, m.getCameraCfg(cameraID))
	if !effectiveCfg.Enabled {
		return nil
	}

	_, _, _, mergeErr := m.processCamera(ctx, cameraID, minAge, effectiveCfg)
	if mergeErr != nil {
		logger.Warn("merge pass error for camera", "camera_id", cameraID, "error", mergeErr)
	}

	return nil
}

// processCamera handles all merge windows for a single camera.
// cfg is the effective merge config for this camera (resolved from global + per-camera override).
func (m *MergeManager) processCamera(ctx context.Context, cameraID string, minAge time.Duration, cfg config.MergeConfig) (merged, segments int, freed int64, err error) {
	unlock := m.lockCamera(cameraID)
	defer unlock()
	remainingLimit := cfg.BatchLimit
	windows, err := m.db.ListCameraMergeWindows(ctx, cameraID, minAge)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("list merge windows: %w", err)
	}

	for _, win := range windows {
		if ctx.Err() != nil {
			break
		}
		if win.SegmentCount < cfg.MinSegmentsToMerge {
			continue
		}

		recs, err := m.db.ListMergeableSegments(ctx, cameraID, win.StartTime, win.EndTime)
		if err != nil {
			logger.Warn("failed to list mergeable segments", "camera_id", cameraID, "error", err)
			continue
		}
		if len(recs) < cfg.MinSegmentsToMerge {
			continue
		}

		// Group by format.
		byFormat := groupByFormat(recs)
		for format, formatRecs := range byFormat {
			if remainingLimit > 0 && len(formatRecs) > remainingLimit {
				formatRecs = formatRecs[:remainingLimit]
			}
			g, s, f := m.mergeFormatGroup(ctx, cameraID, format, formatRecs, cfg)
			merged += g
			segments += s
			freed += f
			if remainingLimit > 0 {
				remainingLimit -= s
			}
			if remainingLimit == 0 {
				break
			}
		}
	}

	// Mark singleton pending segments as merged — they're hour-boundary orphans
	// that will never be merged because their window has only 1 segment.
	singletons, err := m.db.ListSingletonPendingRecordings(ctx, cameraID, minAge)
	if err != nil {
		logger.Warn("failed to list singleton pending recordings", "camera_id", cameraID, "error", err)
	} else if len(singletons) > 0 {
		ids := make([]string, len(singletons))
		for i, r := range singletons {
			ids[i] = r.ID
		}
		if err := m.db.SetMergeStatus(ctx, ids, model.MergeStatusMerged); err != nil {
			logger.Warn("failed to mark singletons as merged", "camera_id", cameraID, "error", err)
		} else {
			logger.Info("marked singleton segments as merged", "camera_id", cameraID, "count", len(singletons))
		}
	}

	return merged, segments, freed, nil
}

// mergeGroupKey returns a key that is identical only for segments that can be
// safely merged into one file. It covers every dimension MergeMP4Segments requires
// to be consistent: codec, video parameter sets (SPS/PPS/VPS), video timescale, and
// audio presence/config/timescale. Segments with any difference get different keys
// and are therefore merged into separate recordings.
func mergeGroupKey(info *SegmentInfo) string {
	key := info.Codec + "\x00" +
		string(info.SPS) + "\x00" +
		string(info.PPS) + "\x00" +
		string(info.VPS) + "\x00" +
		fmt.Sprintf("%d", info.Timescale) + "\x00"
	if info.HasAudio {
		key += "a" + string(info.AudioConfig) + "\x00" + fmt.Sprintf("%d", info.AudioTimescale)
	} else {
		key += "n"
	}
	return key
}

// mergeFormatGroup parses segments, groups by compatibility, and merges eligible groups.
// For MJPEG format, it skips ParseSegment and calls MergeMJPEGSegments directly.
func (m *MergeManager) mergeFormatGroup(ctx context.Context, cameraID, format string, recs []*model.Recording, cfg config.MergeConfig) (merged, segments int, freed int64) {
	// MJPEG segments are directories containing JPEG files — ParseSegment only handles MP4.
	if format == string(model.FormatMJPEG) {
		return m.mergeMJPEGGroup(ctx, cameraID, recs, cfg)
	}

	// Parse all segments.
	type parsedRec struct {
		rec  *model.Recording
		info *SegmentInfo
	}

	var parsed []parsedRec
	var parseFailedIDs []string
	for _, rec := range recs {
		info, err := ParseSegment(rec.FilePath)
		if err != nil {
			logger.Warn("failed to parse segment, marking as failed", "recording_id", rec.ID, "file_path", rec.FilePath, "error", err)
			parseFailedIDs = append(parseFailedIDs, rec.ID)
			continue
		}
		if info.Codec != format {
			continue
		}
		parsed = append(parsed, parsedRec{rec: rec, info: info})
	}

	// Mark parse-failed recordings permanently.
	if len(parseFailedIDs) > 0 {
		if err := m.db.SetMergeStatus(ctx, parseFailedIDs, model.MergeStatusFailed); err != nil {
			logger.Warn("failed to mark parse-failed segments", "error", err)
		} else {
			logger.Info("marked parse-failed segments as merge_status=failed", "count", len(parseFailedIDs))
		}
	}

	// Group by full compatibility key so each group is guaranteed mergeable.
	// Segments that differ in video config (SPS/PPS/VPS), timescale, or audio
	// (presence/config) fall into separate groups and become separate recordings,
	// rather than failing the whole batch.
	groups := make(map[string][]parsedRec)
	for _, p := range parsed {
		groups[mergeGroupKey(p.info)] = append(groups[mergeGroupKey(p.info)], p)
	}

	var smallGroupIDs []string
	for _, group := range groups {
		if len(group) < cfg.MinSegmentsToMerge {
			for _, g := range group {
				smallGroupIDs = append(smallGroupIDs, g.rec.ID)
			}
			continue
		}

		var segmentInfos []*SegmentInfo
		var recordings []*model.Recording
		for _, g := range group {
			segmentInfos = append(segmentInfos, g.info)
			recordings = append(recordings, g.rec)
		}

		ok, oldSize := m.writeMergedGroup(ctx, cameraID, format, recordings, segmentInfos)
		if !ok {
			continue
		}
		merged++
		segments += len(recordings)
		freed += oldSize
	}

	// Mark undersized SPS/PPS groups as permanently failed.
	if len(smallGroupIDs) > 0 {
		if err := m.db.SetMergeStatus(ctx, smallGroupIDs, model.MergeStatusFailed); err != nil {
			logger.Warn("failed to mark undersized group segments", "error", err)
		} else {
			logger.Info("marked undersized SPS/PPS group segments as merge_status=failed", "count", len(smallGroupIDs))
		}
	}

	return merged, segments, freed
}

// writeMergedGroup remuxes recordings+infos into one file and replaces the DB rows.
// Caller must hold the per-camera lock. recordings must be ordered by started_at.
func (m *MergeManager) writeMergedGroup(ctx context.Context, cameraID, format string, recordings []*model.Recording, segmentInfos []*SegmentInfo) (ok bool, freed int64) {
	if len(recordings) == 0 || len(recordings) != len(segmentInfos) {
		return false, 0
	}
	var estSize int64
	for _, r := range recordings {
		estSize += r.FileSize
	}
	total, used, err := m.store.GetDiskUsage()
	if err != nil {
		logger.Warn("failed to get disk usage", "error", err)
		return false, 0
	}
	freeSpace := total - used
	required := estSize * 11 / 10
	if freeSpace < required {
		logger.Warn("insufficient disk space for merge", "camera_id", cameraID, "needed", required, "free", freeSpace)
		return false, 0
	}

	tempPath, finalPath, err := m.store.CreateSegment(cameraID, format)
	if err != nil {
		logger.Warn("failed to create merge output segment", "error", err)
		return false, 0
	}

	if err := MergeMP4Segments(segmentInfos, tempPath); err != nil {
		logger.Error("failed to merge MP4 segments", "camera_id", cameraID, "error", err)
		os.Remove(tempPath)
		return false, 0
	}

	fi, err := os.Stat(tempPath)
	if err != nil || fi.Size() == 0 {
		logger.Error("merged file is empty or missing", "temp_path", tempPath)
		os.Remove(tempPath)
		return false, 0
	}

	if err := m.store.CloseSegment(tempPath, finalPath); err != nil {
		logger.Error("failed to finalize merged segment", "error", err)
		os.Remove(tempPath)
		return false, 0
	}

	var totalDuration float64
	var totalFrames int
	for _, si := range segmentInfos {
		totalDuration += si.TotalDuration.Seconds()
		totalFrames += si.SampleCount
	}
	startTime := recordings[0].StartedAt
	endTime := recordings[len(recordings)-1].EndedAt

	mergedRec := &model.Recording{
		ID:         fmt.Sprintf("%d", time.Now().UnixNano()),
		CameraID:   cameraID,
		FilePath:   finalPath,
		Format:     model.Format(format),
		StartedAt:  startTime,
		EndedAt:    endTime,
		Duration:   totalDuration,
		FileSize:   fi.Size(),
		FrameCount: totalFrames,
		Merged:     true,
	}

	ids := make([]string, len(recordings))
	for i, r := range recordings {
		ids[i] = r.ID
	}
	if err := m.db.MergeAndReplaceRecordings(ctx, mergedRec, ids); err != nil {
		logger.Error("failed to merge and replace recordings", "camera_id", cameraID, "error", err)
		os.Remove(finalPath)
		return false, 0
	}

	var oldSize int64
	for _, r := range recordings {
		oldSize += r.FileSize
		m.store.DeleteFile(r.FilePath)
	}

	logger.Info("merged segments",
		"camera_id", cameraID,
		"segments", len(recordings),
		"duration_s", totalDuration,
		"size_bytes", fi.Size(),
		"freed_bytes", oldSize,
	)
	return true, oldSize
}

// MergeHour appends pending segments in the UTC hour of `at` onto the hour bucket.
// Rolling merge uses this with min_age=0. Caller should not hold the camera lock.
func (m *MergeManager) MergeHour(ctx context.Context, cameraID string, at time.Time) error {
	cfg := m.getGlobalCfg()
	effectiveCfg := config.ResolveMergeConfig(cfg, m.getCameraCfg(cameraID))
	if !effectiveCfg.Enabled {
		return nil
	}
	unlock := m.lockCamera(cameraID)
	defer unlock()
	return m.mergeHourLocked(ctx, cameraID, at, effectiveCfg)
}

func hourWindowUTC(t time.Time) (start, end time.Time) {
	t = t.UTC()
	start = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
	return start, start.Add(time.Hour)
}

func (m *MergeManager) mergeHourLocked(ctx context.Context, cameraID string, at time.Time, cfg config.MergeConfig) error {
	hourStart, hourEnd := hourWindowUTC(at)
	pending, err := m.db.ListMergeableSegments(ctx, cameraID, hourStart, hourEnd)
	if err != nil {
		return fmt.Errorf("list pending: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}
	buckets, err := m.db.ListHourMergedRecordings(ctx, cameraID, hourStart, hourEnd)
	if err != nil {
		return fmt.Errorf("list hour buckets: %w", err)
	}

	byFormat := groupByFormat(pending)
	for format, pendingRecs := range byFormat {
		if format == string(model.FormatMJPEG) || format == "jpeg" {
			rollCfg := cfg
			rollCfg.MinSegmentsToMerge = 2
			m.mergeMJPEGGroup(ctx, cameraID, pendingRecs, rollCfg)
			continue
		}

		type parsedRec struct {
			rec  *model.Recording
			info *SegmentInfo
		}
		parseAll := func(recs []*model.Recording) []parsedRec {
			var out []parsedRec
			for _, rec := range recs {
				info, err := ParseSegment(rec.FilePath)
				if err != nil {
					logger.Warn("rolling skip unreadable segment", "recording_id", rec.ID, "error", err)
					continue
				}
				out = append(out, parsedRec{rec: rec, info: info})
			}
			return out
		}

		pendingParsed := parseAll(pendingRecs)
		bucketParsed := parseAll(filterByFormat(buckets, format))

		pendingGroups := make(map[string][]parsedRec)
		for _, p := range pendingParsed {
			pendingGroups[mergeGroupKey(p.info)] = append(pendingGroups[mergeGroupKey(p.info)], p)
		}
		bucketByKey := make(map[string]parsedRec)
		for _, p := range bucketParsed {
			bucketByKey[mergeGroupKey(p.info)] = p // last in started_at order wins
		}

		for key, group := range pendingGroups {
			var recordings []*model.Recording
			var infos []*SegmentInfo
			if bucket, ok := bucketByKey[key]; ok {
				recordings = append(recordings, bucket.rec)
				infos = append(infos, bucket.info)
			}
			for _, g := range group {
				recordings = append(recordings, g.rec)
				infos = append(infos, g.info)
			}
			if len(recordings) < 2 {
				continue
			}
			m.writeMergedGroup(ctx, cameraID, format, recordings, infos)
		}
	}
	return nil
}

func filterByFormat(recs []*model.Recording, format string) []*model.Recording {
	var out []*model.Recording
	for _, r := range recs {
		if string(r.Format) == format {
			out = append(out, r)
		}
	}
	return out
}

// groupByFormat partitions recordings by their format string.
func groupByFormat(recs []*model.Recording) map[string][]*model.Recording {
	m := make(map[string][]*model.Recording)
	for _, r := range recs {
		f := string(r.Format)
		m[f] = append(m[f], r)
	}
	return m
}

// mergeMJPEGGroup merges MJPEG segment directories into a single merged directory.
// MJPEG segments cannot be parsed by ParseSegment (MP4-only), so this path
// collects all MJPEG recordings and delegates to MergeMJPEGSegments.
func (m *MergeManager) mergeMJPEGGroup(ctx context.Context, cameraID string, recs []*model.Recording, cfg config.MergeConfig) (merged, segments int, freed int64) {
	if len(recs) < cfg.MinSegmentsToMerge {
		return 0, 0, 0
	}

	// Estimate merged size from source recordings.
	var estSize int64
	for _, r := range recs {
		estSize += r.FileSize
	}

	// Check disk space.
	total, used, err := m.store.GetDiskUsage()
	if err != nil {
		logger.Warn("failed to get disk usage", "error", err)
		return 0, 0, 0
	}
	freeSpace := total - used
	required := estSize * 11 / 10
	if freeSpace < required {
		logger.Warn("insufficient disk space for MJPEG merge", "camera_id", cameraID, "needed", required, "free", freeSpace)
		return 0, 0, 0
	}

	// Delegate to MergeMJPEGSegments — handles file moves, source dir deletion, and output dir creation.
	mergedRec, err := MergeMJPEGSegments(ctx, recs, m.store, cameraID)
	if err != nil {
		logger.Error("failed to merge MJPEG segments", "camera_id", cameraID, "error", err)
		return 0, 0, 0
	}

	// Mark as merged.
	mergedRec.Merged = true

	// Atomic: insert merged recording + delete old recordings in single transaction.
	ids := make([]string, len(recs))
	for i, r := range recs {
		ids[i] = r.ID
	}
	if err := m.db.MergeAndReplaceRecordings(ctx, mergedRec, ids); err != nil {
		logger.Error("failed to merge and replace MJPEG recordings", "camera_id", cameraID, "error", err)
		// MergeMJPEGSegments already deleted source dirs, so clean up the orphaned merged dir.
		os.RemoveAll(mergedRec.FilePath)
		return 0, 0, 0
	}

	logger.Info("merged MJPEG segments",
		"camera_id", cameraID,
		"segments", len(recs),
		"duration_s", mergedRec.Duration,
		"size_bytes", mergedRec.FileSize,
		"freed_bytes", estSize,
	)

	return 1, len(recs), estSize
}
