package gb28181

import (
	"context"
	"errors"
	"sync"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

type stubMediaEngine struct {
	startRTPErr     error
	startRTPErr2    error
	startRTPSession *media.StreamSession
	lastRTPReq      media.StartRTPReceiveRequest
	stopRTPErr      error
	stopped         []string
	getStream       *media.StreamInfo
	getErr          error
	calls           int
}

func (s *stubMediaEngine) Start(context.Context) error    { return nil }
func (s *stubMediaEngine) Shutdown(context.Context) error { return nil }
func (s *stubMediaEngine) Ready(context.Context) error    { return nil }
func (s *stubMediaEngine) StartPull(context.Context, media.StartPullRequest) (*media.StreamSession, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) StopPull(context.Context, string) error { return nil }
func (s *stubMediaEngine) StartRTPReceive(_ context.Context, req media.StartRTPReceiveRequest) (*media.StreamSession, error) {
	s.calls++
	s.lastRTPReq = req
	if s.calls == 1 && s.startRTPErr != nil {
		return nil, s.startRTPErr
	}
	if s.calls > 1 && s.startRTPErr2 != nil {
		return nil, s.startRTPErr2
	}
	if s.startRTPSession != nil {
		return s.startRTPSession, nil
	}
	return &media.StreamSession{SessionID: "sess", StreamID: req.StreamID, Port: req.Port}, nil
}
func (s *stubMediaEngine) StopRTPReceive(_ context.Context, sessionID string) error {
	s.stopped = append(s.stopped, sessionID)
	return s.stopRTPErr
}
func (s *stubMediaEngine) KickSession(context.Context, string) error { return nil }
func (s *stubMediaEngine) GetStream(context.Context, string) (*media.StreamInfo, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getStream, nil
}
func (s *stubMediaEngine) ListStreams(context.Context) ([]media.StreamInfo, error) {
	return nil, nil
}
func (s *stubMediaEngine) BuildPlayURL(context.Context, media.PlayURLRequest) (*media.PlayURL, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) AddCustomizePubSession(context.Context, string) (media.CustomizePubSession, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) DelCustomizePubSession(context.Context, media.CustomizePubSession) error {
	return errors.New("not implemented")
}
func (s *stubMediaEngine) SubscribeEvents(context.Context, media.EventFilter) (<-chan media.Event, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) SubscribeRTMPEvents(context.Context) (<-chan media.RTMPEvent, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) SubscribeSRTEvents(context.Context) (<-chan media.SRTEvent, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) SubscribeWHIPEvents(context.Context) (<-chan media.WHIPEvent, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) SubscribeFrames(context.Context, media.SubscribeFramesRequest) (media.FrameSubscription, error) {
	return nil, media.ErrFramesNotSupported
}

type fakeDownloadStore struct {
	mu        sync.Mutex
	nextID    int64
	rows      map[int64]*storage.DownloadRecordRow
	createErr error
	updateErr error
	getErr    error
	updates   []string
}

func newFakeDownloadStore() *fakeDownloadStore {
	return &fakeDownloadStore{rows: make(map[int64]*storage.DownloadRecordRow), nextID: 1}
}

func (f *fakeDownloadStore) CreateDownload(_ context.Context, download *storage.DownloadRecordRow) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return 0, f.createErr
	}
	id := f.nextID
	f.nextID++
	cp := *download
	cp.ID = id
	f.rows[id] = &cp
	return id, nil
}

func (f *fakeDownloadStore) UpdateDownloadStatus(_ context.Context, id int64, status string, fileSize int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, status)
	if f.updateErr != nil {
		return f.updateErr
	}
	if row, ok := f.rows[id]; ok {
		row.Status = status
		row.FileSize = fileSize
	}
	return nil
}

func (f *fakeDownloadStore) GetDownload(_ context.Context, id int64) (*storage.DownloadRecordRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.rows[id], nil
}

func (f *fakeDownloadStore) ListDownloads(context.Context, string, string, int, int) ([]storage.DownloadRecordRow, int, error) {
	return nil, 0, nil
}

type fakeDeviceRepo struct {
	mu            sync.Mutex
	devices       []storage.GB28181DeviceRow
	channels      map[string][]storage.GB28181ChannelRow
	listErr       error
	channelErr    error
	upsertErr     error
	statusCalls   []deviceStatusCall
	regCalls      []string
	onlineCalls   []bool
	ops           []model.OperationLog
	insertOpErr   error
	deleted       []string
	savedChannels map[string][]storage.GB28181ChannelRow
	infoRows      []*storage.GB28181DeviceRow
}

type deviceStatusCall struct {
	deviceID string
	isOnline bool
	address  string
}

func newFakeDeviceRepo() *fakeDeviceRepo {
	return &fakeDeviceRepo{
		channels:      make(map[string][]storage.GB28181ChannelRow),
		savedChannels: make(map[string][]storage.GB28181ChannelRow),
	}
}

func (f *fakeDeviceRepo) ListGB28181Devices(context.Context) ([]storage.GB28181DeviceRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]storage.GB28181DeviceRow, len(f.devices))
	copy(out, f.devices)
	return out, nil
}

func (f *fakeDeviceRepo) GetGB28181Device(context.Context, string) (*storage.GB28181DeviceRow, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeDeviceRepo) UpsertGB28181Device(_ context.Context, device *storage.GB28181DeviceRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return f.upsertErr
	}
	cp := *device
	f.infoRows = append(f.infoRows, &cp)
	return nil
}

func (f *fakeDeviceRepo) UpdateGB28181DeviceStatus(_ context.Context, deviceID string, isOnline bool, address string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls = append(f.statusCalls, deviceStatusCall{deviceID: deviceID, isOnline: isOnline, address: address})
	return nil
}

func (f *fakeDeviceRepo) UpdateGB28181DeviceRegistration(_ context.Context, _, address string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.regCalls = append(f.regCalls, address)
	return nil
}

func (f *fakeDeviceRepo) UpdateGB28181DeviceOnlineStatus(_ context.Context, _ string, isOnline bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onlineCalls = append(f.onlineCalls, isOnline)
	return nil
}

func (f *fakeDeviceRepo) DeleteGB28181Device(_ context.Context, deviceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, deviceID)
	return nil
}

func (f *fakeDeviceRepo) ListGB28181Channels(_ context.Context, deviceID string) ([]storage.GB28181ChannelRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.channelErr != nil {
		return nil, f.channelErr
	}
	return f.channels[deviceID], nil
}

func (f *fakeDeviceRepo) GetGB28181Channel(context.Context, string, string) (*storage.GB28181ChannelRow, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeDeviceRepo) UpsertGB28181Channel(context.Context, *storage.GB28181ChannelRow) error {
	return nil
}

func (f *fakeDeviceRepo) BatchUpsertChannels(_ context.Context, deviceID string, channels []storage.GB28181ChannelRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]storage.GB28181ChannelRow, len(channels))
	copy(cp, channels)
	f.savedChannels[deviceID] = cp
	return nil
}

func (f *fakeDeviceRepo) ReplaceGB28181Channels(context.Context, string, []storage.GB28181ChannelRow) error {
	return nil
}

func (f *fakeDeviceRepo) DeleteGB28181Channel(context.Context, string, string) error { return nil }

func (f *fakeDeviceRepo) DeleteGB28181ChannelsForDevice(context.Context, string) error { return nil }

func (f *fakeDeviceRepo) ListMissingChannels(context.Context, int) ([]storage.GB28181ChannelRow, error) {
	return nil, nil
}

func (f *fakeDeviceRepo) UpdateChannelStatus(context.Context, string, string, string) error {
	return nil
}

func (f *fakeDeviceRepo) IncrementMissingCount(context.Context, string, string) error { return nil }

func (f *fakeDeviceRepo) InsertOperationLog(_ context.Context, log model.OperationLog) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertOpErr != nil {
		return 0, f.insertOpErr
	}
	f.ops = append(f.ops, log)
	return int64(len(f.ops)), nil
}
