// API persistence is split into focused domain contracts and composed only at the handler boundary.
package storage

import (
	"context"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/ai"
	"github.com/lalmax-pro/lalmax-nvr/internal/ai/multimodal"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

// APICoreRepository contains setup and health operations used by API handlers.
type APICoreRepository interface {
	Backup(ctx context.Context, destPath string) error
	Ping(ctx context.Context) error
	SetEventNotifier(fn func(model.Event))
}

// AIHistoryRepository stores AI detections and analyses shown by the API.
type AIHistoryRepository interface {
	InsertAIDetection(ctx context.Context, result ai.DetectionResult) error
	InsertAIAnalysis(ctx context.Context, result interface{}) error
	ListAIAnalyses(ctx context.Context, filter AIHistoryFilter) ([]multimodal.AnalysisResult, int, error)
	ListAIDetections(ctx context.Context, filter AIHistoryFilter) ([]ai.DetectionResult, int, error)
}

// GBAlarmRepository reads alarms received from GB28181 devices.
type GBAlarmRepository interface {
	ListAlarms(ctx context.Context, deviceID string, limit, offset int) ([]AlarmRow, int, error)
}

// AlarmRuleManagementRepository manages event-triggered action rules.
type AlarmRuleManagementRepository interface {
	DeleteAlarmRule(ctx context.Context, id int64) error
	InsertAlarmRule(ctx context.Context, rule *model.AlarmRule) error
	ListAlarmRules(ctx context.Context) ([]model.AlarmRule, error)
}

// ArchiveManagementRepository manages camera archive state and archive statistics.
type ArchiveManagementRepository interface {
	ArchiveAllRecordings(ctx context.Context, cameraID string) (int64, error)
	ArchiveCameraDB(ctx context.Context, cameraID string) error
	GetArchiveGroupStats(ctx context.Context, cameraID string) (count int, totalSize int64, err error)
	GetCameraRecordingStats(ctx context.Context, cameraID string) (count int, totalSize int64, err error)
	SetArchiveRetention(ctx context.Context, cameraID string, retentionDays int) error
	UnarchiveCameraDB(ctx context.Context, cameraID string) error
}

// APICameraRepository manages camera records and their stream bindings.
type APICameraRepository interface {
	BindStreamToCamera(ctx context.Context, streamID, cameraID string) error
	DeleteCamera(ctx context.Context, cameraID string) error
	GetBindingByCameraID(ctx context.Context, cameraID string) (*StreamBinding, error)
	GetCamera(ctx context.Context, cameraID string) (*CameraRow, error)
	GetStreamBinding(ctx context.Context, streamID string) (*StreamBinding, error)
	ListArchivedCameras(ctx context.Context) ([]CameraRow, error)
	ListCameras(ctx context.Context) ([]CameraRow, error)
	ListStreamBindings(ctx context.Context) ([]StreamBinding, error)
	SetCameraStream(ctx context.Context, cameraID, streamID string) error
	UnbindStreamFromCamera(ctx context.Context, streamID string) error
	UpdateCameraMetadata(ctx context.Context, id, description, location, brand, model, serialNumber string, retentionDays int) error
	UpdateCameraProfileName(ctx context.Context, id, profileName string) error
	UpsertCamera(ctx context.Context, id, name, protocol, encoding, url, username, password string, enabled bool, onvifEndpoint, profileToken, streamEncoding string, extras ...string) error
}

// CameraRuntimeRepository persists extra camera configuration.
type CameraRuntimeRepository interface {
	ListCameraConfigs(ctx context.Context) ([]config.CameraConfig, error)
	SaveCameraExtras(ctx context.Context, cam config.CameraConfig) error
}

// CreatedStreamManagementRepository manages user-created stream entries.
type CreatedStreamManagementRepository interface {
	DeleteCreatedStream(ctx context.Context, streamID string) (bool, error)
	GetCreatedStream(ctx context.Context, streamID string) (*CreatedStream, error)
	InsertCreatedStream(ctx context.Context, stream CreatedStream) error
	ListCreatedStreams(ctx context.Context) ([]CreatedStream, error)
	SetCreatedStreamName(ctx context.Context, streamID, name string) error
}

// GBDownloadRepository reads GB28181 recording download jobs.
type GBDownloadRepository interface {
	ListDownloads(ctx context.Context, deviceID, channelID string, limit, offset int) ([]DownloadRecordRow, int, error)
}

// GBDeviceManagementRepository reads GB28181 device records.
type GBDeviceManagementRepository interface {
	GetGB28181Device(ctx context.Context, deviceID string) (*GB28181DeviceRow, error)
}

// GBGroupManagementRepository manages GB28181 business groups and channel membership.
type GBGroupManagementRepository interface {
	AttachChannelsToGroup(ctx context.Context, parentID, businessGroup string, keys []ChannelKey) error
	CreateGBGroup(ctx context.Context, g *GBGroup) (int64, error)
	DeleteGBGroups(ctx context.Context, ids []int64) error
	DetachChannelsFromBusinessGroup(ctx context.Context, businessGroup string) error
	DetachChannelsFromGroup(ctx context.Context, keys []ChannelKey) error
	DetachChannelsFromGroupByParent(ctx context.Context, parentID string) error
	GetGBBusinessGroup(ctx context.Context, businessGroup string) (*GBGroup, error)
	GetGBGroup(ctx context.Context, id int64) (*GBGroup, error)
	GetGBGroupByDeviceID(ctx context.Context, deviceID, businessGroup string) (*GBGroup, error)
	GetGBGroupDescendants(ctx context.Context, id int64) ([]GBGroup, error)
	ListGBGroupsByBusinessGroup(ctx context.Context, businessGroup string) ([]GBGroup, error)
	ListGBGroupsByParent(ctx context.Context, parentID int64) ([]GBGroup, error)
	UpdateGBGroup(ctx context.Context, g *GBGroup) error
}

// GBRegionManagementRepository manages GB28181 administrative regions and channel membership.
type GBRegionManagementRepository interface {
	AddRegionByCivilCode(ctx context.Context, code string) (int, error)
	AttachChannelsToRegion(ctx context.Context, civilCode string, keys []ChannelKey) error
	CreateGBRegion(ctx context.Context, r *GBRegion) (int64, error)
	DeleteGBRegions(ctx context.Context, ids []int64) error
	DetachChannelsFromRegion(ctx context.Context, keys []ChannelKey) error
	DetachChannelsFromRegionByCode(ctx context.Context, civilCode string) error
	GetGBRegion(ctx context.Context, id int64) (*GBRegion, error)
	GetGBRegionByDeviceID(ctx context.Context, deviceID string) (*GBRegion, error)
	GetGBRegionDescendants(ctx context.Context, id int64) ([]GBRegion, error)
	ListChannelsByCivilCode(ctx context.Context, civilCode string) ([]GBChannelBrief, error)
	ListChannelsByParentID(ctx context.Context, parentID string) ([]GBChannelBrief, error)
	ListGBRegionsByParent(ctx context.Context, parentID int64) ([]GBRegion, error)
	SearchChannelsBrief(ctx context.Context, q string, unassignedRegion, unassignedGroup bool) ([]GBChannelBrief, error)
	SyncRegionsFromChannels(ctx context.Context) (int, error)
	UpdateGBRegion(ctx context.Context, r *GBRegion) error
}

// DeviceGroupManagementRepository manages user-defined device groups.
type DeviceGroupManagementRepository interface {
	AddGroupChannel(ctx context.Context, groupID int64, deviceID, channelID string) error
	CreateDeviceGroup(ctx context.Context, group *DeviceGroup) (int64, error)
	DeleteDeviceGroup(ctx context.Context, id int64) error
	GetDeviceGroup(ctx context.Context, id int64) (*DeviceGroup, error)
	GetGroupChannelStats(ctx context.Context, groupID int64) (total int, online int, err error)
	ListDeviceGroups(ctx context.Context) ([]DeviceGroup, error)
	ListGroupChannels(ctx context.Context, groupID int64) ([]DeviceGroupChannelDetail, error)
	RemoveGroupChannel(ctx context.Context, groupID int64, deviceID, channelID string) error
	RemoveGroupChannelsByDeviceID(ctx context.Context, deviceID string) error
	UpdateDeviceGroup(ctx context.Context, group *DeviceGroup) error
}

// MergeConfigurationRepository reads merge progress and updates camera merge settings.
type MergeConfigurationRepository interface {
	CountPendingMerges(ctx context.Context, cameraID string) (int, error)
	ListRecordingTimeline(ctx context.Context, cameraID string, start, end time.Time, limit int) ([]TimelineEntry, error)
	UpsertCameraMerge(ctx context.Context, cameraID string, mergeEnabled *bool, mergeCheckInterval, mergeWindowSize, mergeMinSegmentAge *string, mergeBatchLimit, mergeMinSegmentsToMerge *int, mergeRollingEnabled *bool, mergeRollingDebounce *string) error
}

// OperationLogRepository persists and queries administrative operation logs.
type OperationLogRepository interface {
	InsertOperationLog(ctx context.Context, log model.OperationLog) (int64, error)
	ListOperationLogs(ctx context.Context, filter OperationLogsFilter) ([]model.OperationLog, int, error)
}

// GBPlatformEventRepository reads upstream GB28181 platform status and events.
type GBPlatformEventRepository interface {
	GetPlatformStatus(ctx context.Context) ([]PlatformStatusRow, error)
	ListPlatformEvents(ctx context.Context, platformID int64, eventType string, limit, offset int) ([]PlatformEventRow, int, error)
}

// RecordingManagementRepository manages recording metadata exposed by the API.
type RecordingManagementRepository interface {
	CountRecordingsWithFilter(ctx context.Context, filter model.RecordingFilter) (int, error)
	DeleteRecording(ctx context.Context, id string) error
	DeleteRecordingsBatch(ctx context.Context, ids []string) ([]string, error)
	DeleteRecordingsByCamera(ctx context.Context, cameraID string) (int64, error)
	GetRecording(ctx context.Context, id string) (*model.Recording, error)
	InsertRecording(ctx context.Context, recording *model.Recording) error
	ListDistinctRecordingOwners(ctx context.Context) ([]string, error)
	ListRecordings(ctx context.Context, filter model.RecordingFilter) ([]model.Recording, error)
	SetRecordingLocked(ctx context.Context, id string, locked bool) error
}

// RecordingPlanManagementRepository manages recording schedules.
type RecordingPlanManagementRepository interface {
	DeleteRecordingPlan(ctx context.Context, id string) error
	DeleteRecordingPlanByStream(ctx context.Context, streamID string) error
	GetRecordingPlan(ctx context.Context, id string) (*RecordingPlan, error)
	GetRecordingPlanByStream(ctx context.Context, streamID string) (*RecordingPlan, error)
	ListRecordingPlans(ctx context.Context) ([]RecordingPlan, error)
	UpsertRecordingPlan(ctx context.Context, p *RecordingPlan) error
}

// StatisticsRepository provides recording and camera statistics.
type StatisticsRepository interface {
	CountRecordings(ctx context.Context) (int, error)
	GetAllLastRecordingTimes(ctx context.Context) (map[string]*time.Time, error)
	GetCameraUptimeStats(ctx context.Context, days int) ([]model.CameraUptimeStat, error)
	GetHourlyRecordingStats(ctx context.Context, hours int) ([]model.HourlyStats, error)
	GetLastRecordingTime(ctx context.Context, cameraID string) (*time.Time, error)
	GetRecordingDays(ctx context.Context, cameraID, month string) ([]string, error)
	GetRecordingTrends(ctx context.Context, days int) ([]model.DailyStats, error)
}

// StreamManagementRepository manages stream history, snapshots, and bans.
type StreamManagementRepository interface {
	DeleteStreamHistory(ctx context.Context, streamID string) error
	ListRecentStreamSnapshots(ctx context.Context, since time.Time, limit int) ([]RecentStreamSnapshot, error)
	ListStreamBans(ctx context.Context) ([]StreamBan, error)
	ListStreamHistory(ctx context.Context, streamID string, limit, offset int) ([]StreamHistory, int, error)
}

// UserManagementRepository manages API users and account state.
type UserManagementRepository interface {
	CountUsers(ctx context.Context) (int, error)
	CreateUser(ctx context.Context, u *model.User) error
	DeleteUser(ctx context.Context, id int64) error
	GetUserByID(ctx context.Context, id int64) (*model.User, error)
	GetUserByUsername(ctx context.Context, username string) (*model.User, error)
	HasSuperAdmin(ctx context.Context) (bool, error)
	ListUsers(ctx context.Context) ([]*model.User, error)
	UpdateUser(ctx context.Context, u *model.User) error
}

// VoIPCallRepository reads persisted VoIP call history.
type VoIPCallRepository interface {
	ListVoIPCalls(ctx context.Context, limit, offset int) ([]VoIPCall, int, error)
}

// EventManagementRepository manages product events.
type EventManagementRepository interface {
	AcknowledgeEvent(ctx context.Context, id int64, at time.Time) error
	DeleteEvent(ctx context.Context, id int64) error
	GetEvent(ctx context.Context, id int64) (*model.Event, error)
	ListEvents(ctx context.Context, filter EventsFilter) ([]model.Event, int, error)
}

// FeatureFlagRepository reads and updates runtime feature flags.
type FeatureFlagRepository interface {
	GetFeatureFlags(ctx context.Context) (map[string]bool, error)
	SetFeatureFlag(ctx context.Context, key string, value bool) error
}

// HealthEventRepository reads health events shown by the API.
type HealthEventRepository interface {
	ListHealthEvents(ctx context.Context, filter HealthEventsFilter) ([]model.HealthEvent, int, error)
}

// APIRepository composes the domain-specific persistence contracts used by API handlers.
type APIRepository interface {
	APICoreRepository
	AIHistoryRepository
	GBAlarmRepository
	AlarmRuleManagementRepository
	ArchiveManagementRepository
	APICameraRepository
	CameraRuntimeRepository
	CreatedStreamManagementRepository
	GBDownloadRepository
	GBDeviceManagementRepository
	GBGroupManagementRepository
	GBRegionManagementRepository
	DeviceGroupManagementRepository
	MergeConfigurationRepository
	OperationLogRepository
	GBPlatformEventRepository
	RecordingManagementRepository
	RecordingPlanManagementRepository
	StatisticsRepository
	StreamManagementRepository
	UserManagementRepository
	VoIPCallRepository
	EventManagementRepository
	FeatureFlagRepository
	HealthEventRepository
}

var _ APIRepository = (*DB)(nil)
