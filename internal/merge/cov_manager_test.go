package merge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// covMgrRepo is a storage.MergeRepository fake with independent error injection
// per method, so every manager branch can be driven deterministically.
type covMgrRepo struct {
	mu         sync.Mutex
	windows    []storage.MergeWindow
	segments   []*model.Recording
	singletons []*model.Recording
	buckets    []*model.Recording
	cameraIDs  []string
	recs       map[string]*model.Recording

	pendingErr   error
	windowsErr   error
	segmentsErr  error
	singletonErr error
	bucketsErr   error
	getErr       error
	setErr       error
	mergeErr     error
	growErr      error

	pendingCalls int
	mergeCalls   int
	growCalls    int
	setCalls     int
	lastMerged   *model.Recording
	lastOldIDs   []string
}

func newCovMgrRepo() *covMgrRepo { return &covMgrRepo{recs: map[string]*model.Recording{}} }

// pendingCount reports how many times the pending-camera query ran.
func (r *covMgrRepo) pendingCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pendingCalls
}

func (r *covMgrRepo) ListCameraMergeWindows(context.Context, string, time.Duration, time.Duration) ([]storage.MergeWindow, error) {
	if r.windowsErr != nil {
		return nil, r.windowsErr
	}
	return r.windows, nil
}

func (r *covMgrRepo) ListPendingMergeCameraIDs(context.Context) ([]string, error) {
	r.mu.Lock()
	r.pendingCalls++
	r.mu.Unlock()
	if r.pendingErr != nil {
		return nil, r.pendingErr
	}
	return r.cameraIDs, nil
}

func (r *covMgrRepo) ListMergeableSegments(context.Context, string, time.Time, time.Time) ([]*model.Recording, error) {
	if r.segmentsErr != nil {
		return nil, r.segmentsErr
	}
	return r.segments, nil
}

func (r *covMgrRepo) ListSingletonPendingRecordings(context.Context, string, time.Duration, time.Duration) ([]*model.Recording, error) {
	if r.singletonErr != nil {
		return nil, r.singletonErr
	}
	return r.singletons, nil
}

func (r *covMgrRepo) SetMergeStatus(context.Context, []string, string) error {
	r.setCalls++
	return r.setErr
}

func (r *covMgrRepo) MergeAndReplaceRecordings(_ context.Context, merged *model.Recording, oldIDs []string) error {
	if r.mergeErr != nil {
		return r.mergeErr
	}
	r.mergeCalls++
	r.lastMerged = merged
	r.lastOldIDs = oldIDs
	return nil
}

func (r *covMgrRepo) ListWindowMergedRecordings(context.Context, string, time.Time, time.Time) ([]*model.Recording, error) {
	if r.bucketsErr != nil {
		return nil, r.bucketsErr
	}
	return r.buckets, nil
}

func (r *covMgrRepo) GrowMergedRecording(context.Context, *model.Recording, []string) error {
	if r.growErr != nil {
		return r.growErr
	}
	r.growCalls++
	return nil
}

func (r *covMgrRepo) GetRecording(_ context.Context, id string) (*model.Recording, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.recs[id], nil
}

// covStoreOrDie builds a storage.Manager rooted in a fresh temp dir.
func covStoreOrDie(t *testing.T) *storage.Manager {
	t.Helper()
	m, err := storage.NewManager(filepath.Join(t.TempDir(), "store"))
	require.NoError(t, err)
	return m
}

func covManager(repo storage.MergeRepository, store *storage.Manager, cfg config.MergeConfig,
	perCam func(string) *config.MergeConfig, cameras func() []config.CameraConfig) *MergeManager {
	if perCam == nil {
		perCam = func(string) *config.MergeConfig { return nil }
	}
	return NewMergeManager(repo, store, func() config.MergeConfig { return cfg }, perCam, cameras)
}

func covMJPEGRec(t *testing.T, store *storage.Manager, cameraID, id string, frames, startFrame int, startedAt time.Time) *model.Recording {
	t.Helper()
	tempPath, finalPath, err := store.CreateSegment(cameraID, string(model.FormatMJPEG))
	require.NoError(t, err)
	for i := 0; i < frames; i++ {
		name := fmt.Sprintf("frame%03d.jpg", startFrame+i)
		require.NoError(t, os.WriteFile(filepath.Join(tempPath, name), []byte("jpeg"), 0o644))
	}
	require.NoError(t, store.CloseSegment(tempPath, finalPath))
	return &model.Recording{
		ID:         id,
		CameraID:   cameraID,
		FilePath:   finalPath,
		Format:     model.FormatMJPEG,
		StartedAt:  startedAt,
		EndedAt:    startedAt.Add(time.Minute),
		Duration:   60,
		FileSize:   int64(frames * 4),
		FrameCount: frames,
	}
}

func covH264Rec(t *testing.T, dir, cameraID, id, name string, startedAt time.Time) (*model.Recording, *SegmentInfo) {
	t.Helper()
	path := createH264SegmentWithSamples(t, dir, name, covH264SPS, covH264PPS, [][]byte{covH264IDR, covH264P})
	info, err := ParseSegment(path)
	require.NoError(t, err)
	fi, err := os.Stat(path)
	require.NoError(t, err)
	return &model.Recording{
		ID:         id,
		CameraID:   cameraID,
		FilePath:   path,
		Format:     model.FormatH264,
		StartedAt:  startedAt,
		EndedAt:    startedAt.Add(time.Minute),
		Duration:   60,
		FileSize:   fi.Size(),
		FrameCount: 2,
	}, info
}

// --- diskBudget ----------------------------------------------------------

func TestCovMerge_LoadDiskBudgetError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gone")
	store, err := storage.NewManager(root)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(store.RootDir()))

	m := &MergeManager{store: store}
	b := m.loadDiskBudget()
	require.NotNil(t, b)
	require.False(t, b.ok)
	require.False(t, b.reserve(1))
}

func TestCovMerge_DiskBudgetReserve(t *testing.T) {
	var nilBudget *diskBudget
	require.False(t, nilBudget.reserve(1))

	closed := &diskBudget{free: 100}
	require.False(t, closed.reserve(1))

	b := &diskBudget{free: 100, ok: true}
	require.False(t, b.reserve(-1))
	require.False(t, b.reserve(101))
	require.True(t, b.reserve(40))
	require.Equal(t, int64(60), b.free)
}

// --- PendingCounts -------------------------------------------------------

func TestCovMerge_PendingCountsBranches(t *testing.T) {
	ctx := context.Background()
	repo := newCovMgrRepo()
	repo.cameraIDs = []string{"cam1", "cam2"}
	repo.windows = []storage.MergeWindow{{SegmentCount: 2}, {SegmentCount: 3}}
	mgr := covManager(repo, covStoreOrDie(t), config.MergeConfig{Enabled: true, MinSegmentAge: "not-a-duration"}, nil, nil)

	counts := mgr.PendingCounts(ctx)
	require.Equal(t, 5, counts["cam1"])
	require.Equal(t, 5, counts["cam2"])

	// Camera list error -> empty map.
	repoErr := newCovMgrRepo()
	repoErr.pendingErr = errors.New("db")
	mgrErr := covManager(repoErr, covStoreOrDie(t), config.MergeConfig{Enabled: true}, nil, nil)
	require.Empty(t, mgrErr.PendingCounts(ctx))

	// Window listing error -> camera omitted.
	repoWin := newCovMgrRepo()
	repoWin.cameraIDs = []string{"cam1"}
	repoWin.windowsErr = errors.New("windows")
	mgrWin := covManager(repoWin, covStoreOrDie(t), config.MergeConfig{Enabled: true}, nil, nil)
	require.Empty(t, mgrWin.PendingCounts(ctx))

	// Merge disabled -> camera skipped.
	mgrOff := covManager(repo, covStoreOrDie(t), config.MergeConfig{Enabled: false}, nil, nil)
	require.Empty(t, mgrOff.PendingCounts(ctx))
}

// --- Run -----------------------------------------------------------------

func TestCovMerge_RunCancelsAndTicks(t *testing.T) {
	repo := newCovMgrRepo()
	mgr := covManager(repo, covStoreOrDie(t), config.MergeConfig{
		Enabled:       true,
		CheckInterval: "5ms",
		MinSegmentAge: "0s",
	}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.Run(ctx)
	}()

	// The immediate pass plus at least one ticker pass.
	require.Eventually(t, func() bool { return repo.pendingCount() >= 2 }, 3*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	require.False(t, mgr.Status().LastRunTime.IsZero())
}

func TestCovMerge_RunInvalidIntervalFallsBack(t *testing.T) {
	repo := newCovMgrRepo()
	mgr := covManager(repo, covStoreOrDie(t), config.MergeConfig{
		Enabled:       true,
		CheckInterval: "nonsense",
		MinSegmentAge: "0s",
	}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.Run(ctx)
	}()
	require.Eventually(t, func() bool { return repo.pendingCount() >= 1 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

// --- RunOnce -------------------------------------------------------------

func TestCovMerge_RunOnceListErrorFallsBackToCameras(t *testing.T) {
	ctx := context.Background()
	repo := newCovMgrRepo()
	repo.pendingErr = errors.New("db down")
	repo.windowsErr = errors.New("windows down")
	cameras := []config.CameraConfig{{ID: "enabled-cam", Enabled: true}, {ID: "disabled-cam", Enabled: false}}
	mgr := covManager(repo, covStoreOrDie(t), config.MergeConfig{Enabled: true, MinSegmentAge: "1m"}, nil,
		func() []config.CameraConfig { return cameras })

	require.NoError(t, mgr.RunOnce(ctx))
	status := mgr.Status()
	require.Equal(t, 1, status.ErrorCount)
	require.Equal(t, 0, status.FilesCreated)
}

func TestCovMerge_RunOnceBatchLimitStopsPass(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)
	ctx := context.Background()

	now := time.Now()
	old := now.Truncate(time.Hour).Add(-2 * time.Hour)
	for _, cam := range []string{"cam-a", "cam-b"} {
		require.NoError(t, env.db.UpsertCamera(ctx, cam, cam, "rtsp", "mjpeg", "rtsp://x", "", "", true, "", "", ""))
		env.insertMergeableMJPEGRecording(t, cam+"-1", cam, old, old.Add(30*time.Second), 2, 0)
		env.insertMergeableMJPEGRecording(t, cam+"-2", cam, old.Add(30*time.Second), old.Add(time.Minute), 2, 2)
	}

	cfg := config.MergeConfig{Enabled: true, CheckInterval: "1h", MinSegmentAge: "1m",
		BatchLimit: 2, MinSegmentsToMerge: 2}
	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: "cam-a", Enabled: true}, {ID: "cam-b", Enabled: true}})

	require.NoError(t, mgr.RunOnce(ctx))
	require.Equal(t, 1, mgr.Status().FilesCreated)
	require.Equal(t, 2, mgr.Status().SegmentsMerged)

	recs, err := env.db.ListRecordings(ctx, model.RecordingFilter{})
	require.NoError(t, err)
	require.Len(t, recs, 3, "one camera merged (1 new), the other keeps its 2 segments")
}

// --- MergeCamera ---------------------------------------------------------

func TestCovMerge_MergeCameraBranches(t *testing.T) {
	ctx := context.Background()

	// Disabled -> immediate nil.
	repoOff := newCovMgrRepo()
	off := covManager(repoOff, covStoreOrDie(t), config.MergeConfig{Enabled: false}, nil, nil)
	require.NoError(t, off.MergeCamera(ctx, "cam1"))
	require.Zero(t, repoOff.pendingCalls)

	// Error listing windows -> logged, still nil.
	repoErr := newCovMgrRepo()
	repoErr.windowsErr = errors.New("windows")
	mgrErr := covManager(repoErr, covStoreOrDie(t), config.MergeConfig{Enabled: true, MinSegmentAge: "bad"}, nil, nil)
	require.NoError(t, mgrErr.MergeCamera(ctx, "cam1"))

	// Real recordings get merged.
	env := newMergeTestEnv(t)
	defer env.close(t)
	now := time.Now()
	old := now.Truncate(time.Hour).Add(-2 * time.Hour)
	env.insertMergeableRecording(t, "m1", "cam-m", old, old.Add(30*time.Second))
	env.insertMergeableRecording(t, "m2", "cam-m", old.Add(30*time.Second), old.Add(time.Minute))
	cfg := config.MergeConfig{Enabled: true, MinSegmentAge: "1m", BatchLimit: 100, MinSegmentsToMerge: 2}
	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: "cam-m", Enabled: true}})
	require.NoError(t, mgr.MergeCamera(ctx, "cam-m"))
	recs, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: "cam-m"})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.True(t, recs[0].Merged)
}

// --- processCamera -------------------------------------------------------

func TestCovMerge_ProcessCameraErrorsAndLimits(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Window listing error.
	repo := newCovMgrRepo()
	repo.windowsErr = errors.New("boom")
	mgr := covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)
	_, _, _, err := mgr.processCamera(ctx, "cam1", time.Minute, config.MergeConfig{MinSegmentsToMerge: 2, BatchLimit: 10})
	require.ErrorContains(t, err, "list merge windows")

	// Window below MinSegmentsToMerge is skipped.
	repo = newCovMgrRepo()
	repo.windows = []storage.MergeWindow{{SegmentCount: 1}}
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)
	merged, segs, freed, err := mgr.processCamera(ctx, "cam1", time.Minute, config.MergeConfig{MinSegmentsToMerge: 2, BatchLimit: 10})
	require.NoError(t, err)
	require.Zero(t, merged+segs)
	require.Zero(t, freed)

	// Segment listing error is tolerated.
	repo = newCovMgrRepo()
	repo.windows = []storage.MergeWindow{{SegmentCount: 5}}
	repo.segmentsErr = errors.New("segments")
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)
	_, _, _, err = mgr.processCamera(ctx, "cam1", time.Minute, config.MergeConfig{MinSegmentsToMerge: 2, BatchLimit: 10})
	require.NoError(t, err)

	// Not enough mergeable segments -> skipped.
	repo = newCovMgrRepo()
	repo.windows = []storage.MergeWindow{{SegmentCount: 5}}
	repo.segments = []*model.Recording{{ID: "one"}}
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)
	merged, _, _, err = mgr.processCamera(ctx, "cam1", time.Minute, config.MergeConfig{MinSegmentsToMerge: 2, BatchLimit: 10})
	require.NoError(t, err)
	require.Zero(t, merged)

	// Canceled context breaks out of the window loop.
	repo = newCovMgrRepo()
	repo.windows = []storage.MergeWindow{{SegmentCount: 5}}
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	merged, _, _, err = mgr.processCamera(canceled, "cam1", time.Minute, config.MergeConfig{MinSegmentsToMerge: 2, BatchLimit: 10})
	require.NoError(t, err)
	require.Zero(t, merged)

	// Singleton listing error is tolerated.
	repo = newCovMgrRepo()
	repo.singletonErr = errors.New("singletons")
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)
	_, _, _, err = mgr.processCamera(ctx, "cam1", time.Minute, config.MergeConfig{MinSegmentsToMerge: 2, BatchLimit: 10})
	require.NoError(t, err)

	// Singletons marked merged.
	repo = newCovMgrRepo()
	repo.singletons = []*model.Recording{{ID: "s1"}, {ID: "s2"}}
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)
	_, _, _, err = mgr.processCamera(ctx, "cam1", time.Minute, config.MergeConfig{MinSegmentsToMerge: 2, BatchLimit: 10})
	require.NoError(t, err)
	require.Equal(t, 1, repo.setCalls)

	// Singleton marking failure is tolerated.
	repo = newCovMgrRepo()
	repo.singletons = []*model.Recording{{ID: "s1"}}
	repo.setErr = errors.New("status")
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)
	_, _, _, err = mgr.processCamera(ctx, "cam1", time.Minute, config.MergeConfig{MinSegmentsToMerge: 2, BatchLimit: 10})
	require.NoError(t, err)
	require.Equal(t, 1, repo.setCalls)

	// Batch limit truncates the format group and breaks the loop.
	store := covStoreOrDie(t)
	repo = newCovMgrRepo()
	repo.windows = []storage.MergeWindow{{SegmentCount: 3}}
	repo.segments = []*model.Recording{
		covMJPEGRec(t, store, "cam-mj", "mj1", 1, 0, time.Now()),
		covMJPEGRec(t, store, "cam-mj", "mj2", 1, 1, time.Now()),
		covMJPEGRec(t, store, "cam-mj", "mj3", 1, 2, time.Now()),
	}
	mgr = covManager(repo, store, config.MergeConfig{}, nil, nil)
	merged, segs, freed, err = mgr.processCamera(ctx, "cam-mj", time.Minute, config.MergeConfig{MinSegmentsToMerge: 1, BatchLimit: 1})
	require.NoError(t, err)
	require.Equal(t, 1, merged)
	require.Equal(t, 1, segs)
	require.Greater(t, freed, int64(0))
	require.Equal(t, 1, repo.mergeCalls)

	_ = dir
}

// --- mergeFormatGroup ----------------------------------------------------

func TestCovMerge_MergeFormatGroupBranches(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	rec, info := covH264Rec(t, dir, "cam1", "r1", "r1.mp4", time.Now())

	// Parsed codec differs from the requested format -> skipped.
	repo := newCovMgrRepo()
	mgr := covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)
	merged, segs, freed := mgr.mergeFormatGroup(ctx, "cam1", "h265", []*model.Recording{rec}, config.MergeConfig{MinSegmentsToMerge: 1}, nil)
	require.Zero(t, merged+segs)
	require.Zero(t, freed)

	// Parse failure marks the recording failed, and a failing status write is tolerated.
	broken := &model.Recording{ID: "broken", FilePath: filepath.Join(dir, "missing.mp4"), Format: model.FormatH264}
	repo = newCovMgrRepo()
	repo.setErr = errors.New("status")
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)
	merged, _, _ = mgr.mergeFormatGroup(ctx, "cam1", "h264", []*model.Recording{broken}, config.MergeConfig{MinSegmentsToMerge: 1}, nil)
	require.Zero(t, merged)
	require.Equal(t, 1, repo.setCalls)

	// Parse failure with a working status write.
	repo = newCovMgrRepo()
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)
	merged, _, _ = mgr.mergeFormatGroup(ctx, "cam1", "h264", []*model.Recording{broken}, config.MergeConfig{MinSegmentsToMerge: 1}, nil)
	require.Zero(t, merged)
	require.Equal(t, 1, repo.setCalls)

	// Group smaller than MinSegmentsToMerge stays pending.
	repo = newCovMgrRepo()
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)
	merged, _, _ = mgr.mergeFormatGroup(ctx, "cam1", "h264", []*model.Recording{rec}, config.MergeConfig{MinSegmentsToMerge: 5}, nil)
	require.Zero(t, merged)
	require.Zero(t, repo.mergeCalls)

	// Disk budget refusal skips the group.
	rec2, _ := covH264Rec(t, dir, "cam1", "r2", "r2.mp4", time.Now().Add(time.Minute))
	repo = newCovMgrRepo()
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)
	merged, _, _ = mgr.mergeFormatGroup(ctx, "cam1", "h264",
		[]*model.Recording{rec, rec2}, config.MergeConfig{MinSegmentsToMerge: 2},
		&diskBudget{ok: true, free: 0})
	require.Zero(t, merged)
	require.Zero(t, repo.mergeCalls)

	require.NotNil(t, info)
}

// --- writeMergedGroup ----------------------------------------------------

func TestCovMerge_WriteMergedGroupErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	rec, info := covH264Rec(t, dir, "cam1", "r1", "r1.mp4", time.Now())

	repo := newCovMgrRepo()
	mgr := covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)

	// Empty inputs.
	ok, freed := mgr.writeMergedGroup(ctx, "cam1", "h264", nil, nil, nil)
	require.False(t, ok)
	require.Zero(t, freed)

	// Length mismatch.
	ok, _ = mgr.writeMergedGroup(ctx, "cam1", "h264", []*model.Recording{rec}, nil, nil)
	require.False(t, ok)

	// Budget refuses.
	ok, _ = mgr.writeMergedGroup(ctx, "cam1", "h264",
		[]*model.Recording{rec}, []*SegmentInfo{info}, &diskBudget{ok: true, free: 0})
	require.False(t, ok)

	// Store cannot create the output segment.
	deadRoot := filepath.Join(t.TempDir(), "root")
	deadStore, err := storage.NewManager(deadRoot)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(deadStore.RootDir()))
	require.NoError(t, os.WriteFile(deadStore.RootDir(), []byte("not a dir"), 0o644))
	mgrDead := covManager(repo, deadStore, config.MergeConfig{}, nil, nil)
	ok, _ = mgrDead.writeMergedGroup(ctx, "cam1", "h264",
		[]*model.Recording{rec}, []*SegmentInfo{info}, &diskBudget{ok: true, free: 1 << 40})
	require.False(t, ok)

	// MP4 merge failure.
	badInfo := covCopyInfo(info)
	badInfo.FilePath = ""
	ok, _ = mgr.writeMergedGroup(ctx, "cam1", "h264",
		[]*model.Recording{rec}, []*SegmentInfo{badInfo}, &diskBudget{ok: true, free: 1 << 40})
	require.False(t, ok)

	// DB replace failure cleans up the merged file.
	repo.mergeErr = errors.New("db")
	ok, _ = mgr.writeMergedGroup(ctx, "cam1", "h264",
		[]*model.Recording{rec}, []*SegmentInfo{info}, &diskBudget{ok: true, free: 1 << 40})
	require.False(t, ok)
	repo.mergeErr = nil

	// Success with a nil budget (forced through loadDiskBudget).
	rec2, info2 := covH264Rec(t, dir, "cam1", "r2", "r2.mp4", time.Now().Add(time.Minute))
	rec3, info3 := covH264Rec(t, dir, "cam1", "r3", "r3.mp4", time.Now().Add(2*time.Minute))
	ok, freed = mgr.writeMergedGroup(ctx, "cam1", "h264",
		[]*model.Recording{rec2, rec3}, []*SegmentInfo{info2, info3}, nil)
	require.True(t, ok)
	require.Greater(t, freed, int64(0))
	require.Equal(t, 1, repo.mergeCalls)
	require.NotNil(t, repo.lastMerged)
	require.True(t, repo.lastMerged.Merged)
}

// --- MergeHour / mergeHourLocked ----------------------------------------

func TestCovMerge_MergeHourBranches(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hour := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	// Disabled config.
	repo := newCovMgrRepo()
	mgr := covManager(repo, covStoreOrDie(t), config.MergeConfig{Enabled: false}, nil, nil)
	require.NoError(t, mgr.MergeHour(ctx, "cam1", hour))

	// Pending listing error.
	repo = newCovMgrRepo()
	repo.segmentsErr = errors.New("pending")
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{Enabled: true}, nil, nil)
	require.ErrorContains(t, mgr.MergeHour(ctx, "cam1", hour), "list pending")

	// No pending segments -> nil.
	repo = newCovMgrRepo()
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{Enabled: true}, nil, nil)
	require.NoError(t, mgr.MergeHour(ctx, "cam1", hour))

	// Bucket listing error.
	rec, _ := covH264Rec(t, dir, "cam1", "p1", "p1.mp4", hour.Add(time.Minute))
	repo = newCovMgrRepo()
	repo.segments = []*model.Recording{rec}
	repo.bucketsErr = errors.New("buckets")
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{Enabled: true}, nil, nil)
	require.ErrorContains(t, mgr.MergeHour(ctx, "cam1", hour), "list hour buckets")

	// MJPEG pending segments are merged through the MJPEG path.
	store := covStoreOrDie(t)
	repo = newCovMgrRepo()
	repo.segments = []*model.Recording{
		covMJPEGRec(t, store, "cam-mj", "mj1", 1, 0, hour.Add(time.Minute)),
		covMJPEGRec(t, store, "cam-mj", "mj2", 1, 1, hour.Add(2*time.Minute)),
	}
	mgr = covManager(repo, store, config.MergeConfig{Enabled: true, MinSegmentsToMerge: 2}, nil, nil)
	require.NoError(t, mgr.MergeHour(ctx, "cam-mj", hour))
	require.Equal(t, 1, repo.mergeCalls)

	// Unreadable pending segment is skipped during parsing.
	repo = newCovMgrRepo()
	repo.segments = []*model.Recording{{ID: "gone", FilePath: filepath.Join(dir, "missing.mp4"), Format: model.FormatH264}}
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{Enabled: true, MinSegmentsToMerge: 2}, nil, nil)
	require.NoError(t, mgr.MergeHour(ctx, "cam1", hour))
	require.Zero(t, repo.mergeCalls)

	// A single pending segment without a bucket stays pending.
	recHour, _ := covH264Rec(t, dir, "cam1", "solo", "solo.mp4", hour.Add(time.Minute))
	repo = newCovMgrRepo()
	repo.segments = []*model.Recording{recHour}
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{Enabled: true, MinSegmentsToMerge: 2}, nil, nil)
	require.NoError(t, mgr.MergeHour(ctx, "cam1", hour))
	require.Zero(t, repo.mergeCalls)

	// Non-appendable bucket falls back to a full remux. The muxer output is
	// appendable by design, so append trailing junk to break that layout.
	bucket, bucketInfo := covH264Rec(t, dir, "cam1", "bucket", "bucket.mp4", hour.Add(time.Minute))
	bf, err := os.OpenFile(bucket.FilePath, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = bf.Write([]byte("junk"))
	require.NoError(t, err)
	require.NoError(t, bf.Close())
	p1, _ := covH264Rec(t, dir, "cam1", "p1", "hour-p1.mp4", hour.Add(2*time.Minute))
	p2, _ := covH264Rec(t, dir, "cam1", "p2", "hour-p2.mp4", hour.Add(3*time.Minute))
	repo = newCovMgrRepo()
	repo.buckets = []*model.Recording{bucket}
	repo.segments = []*model.Recording{p1, p2}
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{Enabled: true, MinSegmentsToMerge: 2, BatchLimit: 100}, nil, nil)
	require.NoError(t, mgr.MergeHour(ctx, "cam1", hour))
	require.Equal(t, 1, repo.mergeCalls, "non-appendable bucket should be remuxed")
	require.NotNil(t, repo.lastMerged)
	require.Equal(t, 3, len(repo.lastOldIDs))
	require.NotNil(t, bucketInfo)

	// Corrupt merge journal for the bucket is logged and the append fails
	// with a non-sentinel error (warn branch).
	bucket2, _ := covH264Rec(t, dir, "cam1", "bucket2", "bucket2.mp4", hour.Add(time.Minute))
	require.NoError(t, os.WriteFile(journalPath(bucket2.FilePath), []byte("XXXX"), 0o644))
	repo = newCovMgrRepo()
	repo.buckets = []*model.Recording{bucket2}
	repo.segments = []*model.Recording{
		mustCovRec(t, dir, "cam1", "j1", "j1.mp4", hour.Add(2*time.Minute)),
		mustCovRec(t, dir, "cam1", "j2", "j2.mp4", hour.Add(3*time.Minute)),
	}
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{Enabled: true, MinSegmentsToMerge: 2, BatchLimit: 100}, nil, nil)
	require.NoError(t, mgr.MergeHour(ctx, "cam1", hour))
	require.Zero(t, repo.mergeCalls)
}

func mustCovRec(t *testing.T, dir, cameraID, id, name string, at time.Time) *model.Recording {
	t.Helper()
	rec, _ := covH264Rec(t, dir, cameraID, id, name, at)
	return rec
}

// --- appendOntoBucket ----------------------------------------------------

func TestCovMerge_AppendOntoBucketErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	_, _, base := covHourFile(t)

	bucket := &model.Recording{ID: "bucket", CameraID: "cam1", FilePath: base.FilePath, FileSize: 10}

	repo := newCovMgrRepo()
	mgr := covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)

	// Nil bucket / mismatched lengths.
	require.ErrorContains(t, mgr.appendOntoBucket(ctx, "cam1", nil, []*model.Recording{{}}, []*SegmentInfo{{}}, nil), "nothing to append")
	require.ErrorContains(t, mgr.appendOntoBucket(ctx, "cam1", bucket, nil, nil, nil), "nothing to append")
	require.ErrorContains(t, mgr.appendOntoBucket(ctx, "cam1", bucket, []*model.Recording{{}}, nil, nil), "nothing to append")

	// Corrupt journal blocks the append.
	require.NoError(t, os.WriteFile(journalPath(base.FilePath), []byte("XXXX"), 0o644))
	addRec, addInfo := covH264Rec(t, dir, "cam1", "add1", "add1.mp4", time.Now())
	require.Error(t, mgr.appendOntoBucket(ctx, "cam1", bucket, []*model.Recording{addRec}, []*SegmentInfo{addInfo}, nil))
	require.NoError(t, os.Remove(journalPath(base.FilePath)))

	// Unreadable bucket file.
	missingBucket := &model.Recording{ID: "b", FilePath: filepath.Join(dir, "missing.mp4")}
	require.Error(t, mgr.appendOntoBucket(ctx, "cam1", missingBucket, []*model.Recording{addRec}, []*SegmentInfo{addInfo}, nil))

	// Non-appendable bucket (trailing junk).
	trailing := filepath.Join(dir, "trailing.mp4")
	require.NoError(t, copyFileForTest(base.FilePath, trailing))
	f, err := os.OpenFile(trailing, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.Write([]byte("junk"))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	trailingBucket := &model.Recording{ID: "t", FilePath: trailing}
	err = mgr.appendOntoBucket(ctx, "cam1", trailingBucket, []*model.Recording{addRec}, []*SegmentInfo{addInfo}, nil)
	require.ErrorIs(t, err, errAppendNotPossible)

	// Disk budget refuses the append.
	err = mgr.appendOntoBucket(ctx, "cam1", bucket, []*model.Recording{addRec}, []*SegmentInfo{addInfo},
		&diskBudget{ok: true, free: 0})
	require.ErrorIs(t, err, errMergeNeedSpace)

	// Source file missing: AppendMP4Samples fails after the journal is written.
	badAdd := covCopyInfo(addInfo)
	badAdd.FilePath = filepath.Join(dir, "missing-add.mp4")
	err = mgr.appendOntoBucket(ctx, "cam1", bucket, []*model.Recording{addRec}, []*SegmentInfo{badAdd}, nil)
	require.Error(t, err)
	require.NotErrorIs(t, err, errAppendNotPossible)
	require.NoError(t, os.Remove(journalPath(base.FilePath)))

	// Hide failure after a successful append: the consumed path is occupied by
	// a non-empty directory, so the rename fails and the append is rolled back.
	_, _, base2 := covHourFile(t)
	bucket2 := &model.Recording{ID: "bucket2", CameraID: "cam1", FilePath: base2.FilePath}
	addRec2, addInfo2 := covH264Rec(t, dir, "cam1", "add2", "add2.mp4", time.Now())
	require.NoError(t, os.Mkdir(consumedPath(addRec2.FilePath), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(consumedPath(addRec2.FilePath), "child"), []byte("x"), 0o644))
	err = mgr.appendOntoBucket(ctx, "cam1", bucket2, []*model.Recording{addRec2}, []*SegmentInfo{addInfo2}, nil)
	require.Error(t, err)
	_, statErr := os.Stat(journalPath(base2.FilePath))
	require.ErrorIs(t, statErr, os.ErrNotExist, "journal must be cleaned up on rollback")

	// DB grow failure rolls the append back.
	_, _, base3 := covHourFile(t)
	bucket3 := &model.Recording{ID: "bucket3", CameraID: "cam1", FilePath: base3.FilePath}
	addRec3, addInfo3 := covH264Rec(t, dir, "cam1", "add3", "add3.mp4", time.Now())
	repo.growErr = errors.New("grow")
	err = mgr.appendOntoBucket(ctx, "cam1", bucket3, []*model.Recording{addRec3}, []*SegmentInfo{addInfo3}, nil)
	require.ErrorContains(t, err, "grow")
	repo.growErr = nil

	// Successful append grows the bucket row.
	_, _, base4 := covHourFile(t)
	bucket4 := &model.Recording{ID: "bucket4", CameraID: "cam1", FilePath: base4.FilePath,
		StartedAt: time.Now().Add(-time.Hour), EndedAt: time.Now().Add(-time.Hour)}
	addRec4, addInfo4 := covH264Rec(t, dir, "cam1", "add4", "add4.mp4", time.Now())
	require.NoError(t, mgr.appendOntoBucket(ctx, "cam1", bucket4, []*model.Recording{addRec4}, []*SegmentInfo{addInfo4}, nil))
	require.Equal(t, 1, repo.growCalls)
	_, statErr = os.Stat(addRec4.FilePath)
	require.ErrorIs(t, statErr, os.ErrNotExist, "consumed source must be removed")
}

// --- mergeMJPEGGroup -----------------------------------------------------

func TestCovMerge_MergeMJPEGGroupBranches(t *testing.T) {
	ctx := context.Background()
	store := covStoreOrDie(t)

	repo := newCovMgrRepo()
	mgr := covManager(repo, store, config.MergeConfig{}, nil, nil)

	// Too few segments.
	merged, segs, freed := mgr.mergeMJPEGGroup(ctx, "cam1", []*model.Recording{covMJPEGRec(t, store, "cam1", "a", 1, 0, time.Now())},
		config.MergeConfig{MinSegmentsToMerge: 2}, nil)
	require.Zero(t, merged+segs)
	require.Zero(t, freed)

	// Budget refusal.
	recs := []*model.Recording{
		covMJPEGRec(t, store, "cam1", "b", 1, 1, time.Now()),
		covMJPEGRec(t, store, "cam1", "c", 1, 2, time.Now()),
	}
	merged, _, _ = mgr.mergeMJPEGGroup(ctx, "cam1", recs, config.MergeConfig{MinSegmentsToMerge: 2},
		&diskBudget{ok: true, free: 0})
	require.Zero(t, merged)

	// Source directory missing -> MergeMJPEGSegments fails.
	bad := []*model.Recording{
		{ID: "x", FilePath: filepath.Join(t.TempDir(), "missing-a"), Format: model.FormatMJPEG},
		{ID: "y", FilePath: filepath.Join(t.TempDir(), "missing-b"), Format: model.FormatMJPEG},
	}
	merged, _, _ = mgr.mergeMJPEGGroup(ctx, "cam1", bad, config.MergeConfig{MinSegmentsToMerge: 2}, nil)
	require.Zero(t, merged)

	// DB replace failure removes the orphaned merged directory.
	recs = []*model.Recording{
		covMJPEGRec(t, store, "cam1", "d", 1, 3, time.Now()),
		covMJPEGRec(t, store, "cam1", "e", 1, 4, time.Now()),
	}
	repo.mergeErr = errors.New("db")
	merged, _, _ = mgr.mergeMJPEGGroup(ctx, "cam1", recs, config.MergeConfig{MinSegmentsToMerge: 2}, nil)
	require.Zero(t, merged)
	repo.mergeErr = nil

	// Success.
	recs = []*model.Recording{
		covMJPEGRec(t, store, "cam1", "f", 1, 5, time.Now()),
		covMJPEGRec(t, store, "cam1", "g", 1, 6, time.Now()),
	}
	merged, segs, freed = mgr.mergeMJPEGGroup(ctx, "cam1", recs, config.MergeConfig{MinSegmentsToMerge: 2}, nil)
	require.Equal(t, 1, merged)
	require.Equal(t, 2, segs)
	require.Greater(t, freed, int64(0))
	require.Equal(t, 1, repo.mergeCalls)
}

// --- misc ----------------------------------------------------------------

func TestCovMerge_MergeWindowUTCInvalidWindow(t *testing.T) {
	at := time.Date(2026, 5, 1, 14, 37, 11, 0, time.UTC)
	start, end := mergeWindowUTC(at, 0)
	require.Equal(t, time.Date(2026, 5, 1, 14, 0, 0, 0, time.UTC), start)
	require.Equal(t, time.Date(2026, 5, 1, 15, 0, 0, 0, time.UTC), end)

	start, end = mergeWindowUTC(at, -time.Hour)
	require.Equal(t, time.Hour, end.Sub(start))
}

func TestCovMerge_MergeCandidateIDsWithoutCameraLister(t *testing.T) {
	ctx := context.Background()
	repo := newCovMgrRepo()
	repo.cameraIDs = []string{"a", "b"}
	mgr := covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil, nil)
	ids, err := mgr.mergeCandidateIDs(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, ids)

	// Disabled cameras are filtered out.
	mgr = covManager(repo, covStoreOrDie(t), config.MergeConfig{}, nil,
		func() []config.CameraConfig { return []config.CameraConfig{{ID: "b", Enabled: false}} })
	ids, err = mgr.mergeCandidateIDs(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, ids)
}

func TestCovMerge_LockCameraReusesLock(t *testing.T) {
	mgr := covManager(newCovMgrRepo(), covStoreOrDie(t), config.MergeConfig{}, nil, nil)
	unlock := mgr.lockCamera("cam1")
	unlock()
	unlock2 := mgr.lockCamera("cam1")
	unlock2()
	require.Len(t, mgr.cameraLocks, 1)
}
