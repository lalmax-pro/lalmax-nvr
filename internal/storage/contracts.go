package storage

import (
	"context"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

// RelayTaskRepository stores the persistent state of relay push tasks.
// The relay package depends on this contract rather than a concrete database
// implementation, keeping SQL and database-specific details inside storage.
type RelayTaskRepository interface {
	SaveRelayTask(ctx context.Context, task RelayTaskRecord) error
	GetRelayTask(ctx context.Context, taskID string) (*RelayTaskRecord, error)
	ListRelayTasks(ctx context.Context) ([]RelayTaskRecord, error)
	DeleteRelayTask(ctx context.Context, taskID string) error
}

// CameraRepository contains persistence operations for camera configuration and
// metadata. Runtime status remains owned by the camera/health services.
type CameraRepository interface {
	GetCamera(ctx context.Context, cameraID string) (*CameraRow, error)
	UpsertCamera(ctx context.Context, id, name, protocol, encoding, url, username, password string, enabled bool, onvifEndpoint, profileToken, streamEncoding string, extras ...string) error
	UpdateCameraMetadata(ctx context.Context, id, description, location, brand, model, serialNumber string, retentionDays int) error
	UpdateCameraProfileName(ctx context.Context, id, profileName string) error
	UpdateCameraActivation(ctx context.Context, id, state string) error
	UpdateCameraStableID(ctx context.Context, id, stableID string) error
	SaveCameraExtras(ctx context.Context, camera config.CameraConfig) error
	ListCameraConfigs(ctx context.Context) ([]config.CameraConfig, error)
}

// StreamBindingRepository manages the relationship between an input stream and
// its camera record.
type StreamBindingRepository interface {
	BindStreamToCamera(ctx context.Context, streamID, cameraID string) error
	GetBindingByCameraID(ctx context.Context, cameraID string) (*StreamBinding, error)
	SetCameraStream(ctx context.Context, cameraID, streamID string) error
}

// CameraArchiveRepository manages soft-archiving camera records.
type CameraArchiveRepository interface {
	ArchiveCameraDB(ctx context.Context, cameraID string) error
	UnarchiveCameraDB(ctx context.Context, cameraID string) error
}

// RecordingArchiveRepository archives recordings together with a camera.
type RecordingArchiveRepository interface {
	ArchiveAllRecordings(ctx context.Context, cameraID string) (int64, error)
	UnarchiveAllRecordings(ctx context.Context, cameraID string) (int64, error)
}

// RecordingRepository contains the write operations required by recorders.
type RecordingRepository interface {
	InsertRecording(ctx context.Context, recording *model.Recording) error
	InsertRecordingWithRetry(ctx context.Context, recording *model.Recording, maxRetries int, backoff time.Duration) error
}

// RecordingPlanRepository provides the desired recording state computed from
// persisted plans.
type RecordingPlanRepository interface {
	DesiredRecordingStreams(ctx context.Context) (map[string]bool, error)
	ListRecordingPlans(ctx context.Context) ([]RecordingPlan, error)
}

// RecordingStateRepository is the scheduler's minimal read-only plan contract.
type RecordingStateRepository interface {
	DesiredRecordingStreams(ctx context.Context) (map[string]bool, error)
}

// StreamBanRepository stores stream publish bans.
type StreamBanRepository interface {
	GetStreamBan(ctx context.Context, streamID string) (*StreamBan, error)
	InsertStreamBan(ctx context.Context, ban *StreamBan) error
	DeleteStreamBan(ctx context.Context, streamID string) error
	ListStreamBans(ctx context.Context) ([]StreamBan, error)
}

// StreamHistoryRepository stores publisher and pull session history.
type StreamHistoryRepository interface {
	InsertStreamHistory(ctx context.Context, history *StreamHistory) error
	FinishStreamHistory(ctx context.Context, sessionID string, endedAt time.Time, bytesRead, bytesWritten uint64) error
}

// EventRepository archives product events.
type EventRepository interface {
	InsertEvent(ctx context.Context, event model.Event) (int64, error)
}

// UploadRepository validates camera ownership and saves upload metadata.
type UploadRepository interface {
	CameraRepository
	RecordingRepository
}

// CameraDiscoveryRepository supports matching discovered endpoints against
// existing cameras and refreshing their device metadata.
type CameraDiscoveryRepository interface {
	FindCameraBySerial(ctx context.Context, serial string) (*CameraRow, error)
	FindCameraByEndpoint(ctx context.Context, endpoint string) (*CameraRow, error)
	UpdateCameraMetadata(ctx context.Context, id, description, location, brand, model, serialNumber string, retentionDays int) error
	UpdateCameraStableID(ctx context.Context, id, stableID string) error
	UpdateCameraActivation(ctx context.Context, id, state string) error
}

// AlarmRuleRepository reads rules consumed by the event linkage engine.
type AlarmRuleRepository interface {
	ListAlarmRules(ctx context.Context) ([]model.AlarmRule, error)
}

// IPTVRepository stores playlist imports, sources, channels, and their related
// recording-plan state.
type IPTVRepository interface {
	InsertIPTVImportJob(ctx context.Context, job IPTVImportJob) error
	UpdateIPTVImportJob(ctx context.Context, job IPTVImportJob) error
	GetIPTVImportJob(ctx context.Context, id string) (*IPTVImportJob, error)
	InsertIPTVImportItems(ctx context.Context, items []IPTVImportItem) error
	UpdateIPTVImportItem(ctx context.Context, item IPTVImportItem) error
	ListIPTVImportItems(ctx context.Context, jobID, status, q string) ([]IPTVImportItem, error)
	GetIPTVImportItem(ctx context.Context, id string) (*IPTVImportItem, error)
	GetIPTVImportItemsByIDs(ctx context.Context, jobID string, ids []string) ([]IPTVImportItem, error)
	InsertIPTVSource(ctx context.Context, src IPTVSource) error
	ListIPTVSources(ctx context.Context) ([]IPTVSource, error)
	GetIPTVSource(ctx context.Context, id string) (*IPTVSource, error)
	DeleteIPTVSource(ctx context.Context, id string) error
	UpsertIPTVChannel(ctx context.Context, ch IPTVChannel) error
	ListIPTVChannels(ctx context.Context, sourceID, group, q string, favorite *bool) ([]IPTVChannel, error)
	GetIPTVChannel(ctx context.Context, id string) (*IPTVChannel, error)
	UpdateIPTVChannelMeta(ctx context.Context, id, name string, enabled, favorite, publishEnabled bool) error
	DeleteIPTVChannel(ctx context.Context, id string) error
	ListIPTVGroups(ctx context.Context) ([]string, error)
	DeleteRecordingPlanByStream(ctx context.Context, streamID string) error
}

// DLNARepository provides the catalog data exposed by the DLNA content server.
type DLNARepository interface {
	ListCameras(ctx context.Context) ([]CameraRow, error)
	ListRecordings(ctx context.Context, filter model.RecordingFilter) ([]model.Recording, error)
	ListCreatedStreams(ctx context.Context) ([]CreatedStream, error)
	GetRecording(ctx context.Context, id string) (*model.Recording, error)
}

// WebDAVRepository covers camera discovery and recording registration used by
// the writable WebDAV endpoints.
type WebDAVRepository interface {
	ListCameras(ctx context.Context) ([]CameraRow, error)
	UpsertCamera(ctx context.Context, id, name, protocol, encoding, url, username, password string, enabled bool, onvifEndpoint, profileToken, streamEncoding string, extras ...string) error
	RecordingRepository
}

// CleanupRepository exposes retention cleanup and recovery operations.
type CleanupRepository interface {
	CountRecordings(ctx context.Context) (int, error)
	ListCameras(ctx context.Context) ([]CameraRow, error)
	ListExpiredRecordingsByCamera(ctx context.Context, cameraID string, retentionDays int) ([]model.Recording, error)
	ListOldestRecordings(ctx context.Context, limit int) ([]model.Recording, error)
	DeleteRecording(ctx context.Context, id string) error
	ListArchivedCameras(ctx context.Context) ([]CameraRow, error)
	ListExpiredArchivedRecordingsByCamera(ctx context.Context, cameraID string, retentionDays int) ([]model.Recording, error)
	CountRecordingsByCamera(ctx context.Context, cameraID string) (int, error)
	DeleteCamera(ctx context.Context, cameraID string) error
	DeleteHealthEventsBefore(ctx context.Context, before time.Time) (int64, error)
	ListRecordingPathsByCamera(ctx context.Context, cameraID string) (map[string]bool, error)
	ListPendingMJPEGRecordings(ctx context.Context, cameraID string) ([]model.Recording, error)
	SetMergeStatus(ctx context.Context, ids []string, status string) error
	RepairZeroDurationRecordings(ctx context.Context) ([]model.Recording, error)
	UpdateRecordingDuration(ctx context.Context, id string, duration float64, endedAt time.Time) error
}

// MergeRepository provides the transaction-level operations required to
// select, merge, and replace recording segments.
type MergeRepository interface {
	ListCameraMergeWindows(ctx context.Context, cameraID string, minAge, window time.Duration) ([]MergeWindow, error)
	ListPendingMergeCameraIDs(ctx context.Context) ([]string, error)
	ListMergeableSegments(ctx context.Context, cameraID string, windowStart, windowEnd time.Time) ([]*model.Recording, error)
	ListSingletonPendingRecordings(ctx context.Context, cameraID string, minAge, window time.Duration) ([]*model.Recording, error)
	SetMergeStatus(ctx context.Context, ids []string, status string) error
	MergeAndReplaceRecordings(ctx context.Context, merged *model.Recording, oldIDs []string) error
	ListWindowMergedRecordings(ctx context.Context, cameraID string, windowStart, windowEnd time.Time) ([]*model.Recording, error)
	GrowMergedRecording(ctx context.Context, rec *model.Recording, deleteIDs []string) error
	GetRecording(ctx context.Context, id string) (*model.Recording, error)
}

// GB28181DeviceRepository persists registered GB28181 devices.
type GB28181DeviceRepository interface {
	ListGB28181Devices(ctx context.Context) ([]GB28181DeviceRow, error)
	GetGB28181Device(ctx context.Context, deviceID string) (*GB28181DeviceRow, error)
	UpsertGB28181Device(ctx context.Context, device *GB28181DeviceRow) error
	UpdateGB28181DeviceStatus(ctx context.Context, deviceID string, isOnline bool, address string) error
	UpdateGB28181DeviceRegistration(ctx context.Context, deviceID, address string) error
	UpdateGB28181DeviceOnlineStatus(ctx context.Context, deviceID string, isOnline bool) error
	DeleteGB28181Device(ctx context.Context, deviceID string) error
}

// GB28181ChannelRepository persists discovered channel inventory and status.
type GB28181ChannelRepository interface {
	ListGB28181Channels(ctx context.Context, deviceID string) ([]GB28181ChannelRow, error)
	GetGB28181Channel(ctx context.Context, deviceID, channelID string) (*GB28181ChannelRow, error)
	UpsertGB28181Channel(ctx context.Context, channel *GB28181ChannelRow) error
	BatchUpsertChannels(ctx context.Context, deviceID string, channels []GB28181ChannelRow) error
	ReplaceGB28181Channels(ctx context.Context, deviceID string, channels []GB28181ChannelRow) error
	DeleteGB28181Channel(ctx context.Context, deviceID, channelID string) error
	DeleteGB28181ChannelsForDevice(ctx context.Context, deviceID string) error
	ListMissingChannels(ctx context.Context, threshold int) ([]GB28181ChannelRow, error)
	UpdateChannelStatus(ctx context.Context, deviceID, channelID, status string) error
	IncrementMissingCount(ctx context.Context, deviceID, channelID string) error
}

// GB28181RegionRepository reads administrative region hierarchy for catalog
// reporting.
type GB28181RegionRepository interface {
	GetGBRegionByDeviceID(ctx context.Context, deviceID string) (*GBRegion, error)
}

// GB28181GroupRepository reads business group hierarchy for catalog reporting.
type GB28181GroupRepository interface {
	GetGBGroupByDeviceID(ctx context.Context, deviceID, businessGroup string) (*GBGroup, error)
	GetGBBusinessGroup(ctx context.Context, businessGroup string) (*GBGroup, error)
}

// GB28181AlarmRepository persists alarms received from devices.
type GB28181AlarmRepository interface {
	CreateAlarm(ctx context.Context, alarm *AlarmRow) (int64, error)
	ListAlarms(ctx context.Context, deviceID string, limit, offset int) ([]AlarmRow, int, error)
}

// GB28181DownloadRepository persists device recording download state.
type GB28181DownloadRepository interface {
	CreateDownload(ctx context.Context, download *DownloadRecordRow) (int64, error)
	UpdateDownloadStatus(ctx context.Context, id int64, status string, fileSize int64) error
	GetDownload(ctx context.Context, id int64) (*DownloadRecordRow, error)
	ListDownloads(ctx context.Context, deviceID, channelID string, limit, offset int) ([]DownloadRecordRow, int, error)
}

// GB28181PlatformRepository persists upstream platform configuration and
// runtime events used by the SIP platform manager.
type GB28181PlatformRepository interface {
	ListPlatforms(ctx context.Context) ([]PlatformRow, error)
	CreatePlatform(ctx context.Context, platform *PlatformRow) (int64, error)
	DeletePlatform(ctx context.Context, id int64) error
	UpdatePlatformStatus(ctx context.Context, id int64, status bool) error
	ListPlatformChannels(ctx context.Context, platformID int64, shared *bool) ([]PlatformChannelRow, error)
	AddPlatformEvent(ctx context.Context, event PlatformEventRow) error
}

// GB28181PlatformCatalogRepository includes the inventory lookups required to
// build catalog trees while keeping platform signaling persistence abstract.
type GB28181PlatformCatalogRepository interface {
	GB28181PlatformRepository
	GB28181RegionRepository
	GB28181GroupRepository
	GB28181ChannelRepository
}

// GB28181DeviceStoreRepository is the persistence surface used by the runtime
// device cache for restoring and updating devices and their channels.
type GB28181DeviceStoreRepository interface {
	GB28181DeviceRepository
	GB28181ChannelRepository
	GB28181OperationLogRepository
}

// GB28181OperationLogRepository records device management operations.
type GB28181OperationLogRepository interface {
	InsertOperationLog(ctx context.Context, log model.OperationLog) (int64, error)
}

// GB28181Repository composes the GB28181-specific persistence contracts used
// when wiring the signaling server. Individual managers accept narrower
// contracts from this set.
type GB28181Repository interface {
	GB28181DeviceRepository
	GB28181ChannelRepository
	GB28181RegionRepository
	GB28181GroupRepository
	GB28181AlarmRepository
	GB28181DownloadRepository
	GB28181PlatformRepository
	GB28181OperationLogRepository
}

// CameraManagerRepository is the composition needed by CameraManager. Keeping
// the smaller domain contracts above lets other components depend on less.
type CameraManagerRepository interface {
	CameraRepository
	StreamBindingRepository
	CameraArchiveRepository
	RecordingArchiveRepository
	RecordingRepository
}

// CameraSyncRepository is the smaller contract needed when importing legacy
// camera configuration into persistent storage.
type CameraSyncRepository interface {
	UpsertCamera(ctx context.Context, id, name, protocol, encoding, url, username, password string, enabled bool, onvifEndpoint, profileToken, streamEncoding string, extras ...string) error
	SaveCameraExtras(ctx context.Context, camera config.CameraConfig) error
	ListCameraConfigs(ctx context.Context) ([]config.CameraConfig, error)
	BindStreamToCamera(ctx context.Context, streamID, cameraID string) error
}

var (
	_ RelayTaskRepository       = (*DB)(nil)
	_ CameraManagerRepository   = (*DB)(nil)
	_ CameraSyncRepository      = (*DB)(nil)
	_ RecordingRepository       = (*DB)(nil)
	_ RecordingPlanRepository   = (*DB)(nil)
	_ RecordingStateRepository  = (*DB)(nil)
	_ StreamBanRepository       = (*DB)(nil)
	_ StreamHistoryRepository   = (*DB)(nil)
	_ EventRepository           = (*DB)(nil)
	_ UploadRepository          = (*DB)(nil)
	_ CameraDiscoveryRepository = (*DB)(nil)
	_ AlarmRuleRepository       = (*DB)(nil)
	_ IPTVRepository            = (*DB)(nil)
	_ DLNARepository            = (*DB)(nil)
	_ CleanupRepository         = (*DB)(nil)
	_ MergeRepository           = (*DB)(nil)
	_ GB28181Repository         = (*DB)(nil)
)
