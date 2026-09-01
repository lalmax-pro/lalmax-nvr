/**
 * API barrel — re-exports everything so existing `$lib/api` imports work unchanged.
 */

// Client — auth, base fetch wrappers
export {
  storeCredentials,
  getCredentials,
  clearCredentials,
  isAuthenticated,
  login,
  logout,
  healthCheck,
  getSystemStats,
  getLocalNetworkInterfaces,
  getAuthHeader,
  getAuthToken,
  API_BASE,
  apiRequest,
  apiRequestBlob,
  ApiRequestError,
  setupApi,
  browseSetupDirectories,
  getSystemMetricsHistory,
  getHourlyStats,
  getCameraUptimeStats,
  getAPIObservability,
} from './client';

export type {
  AuthCredentials,
  LoginResponse,
  ApiError,
  HealthCheck,
  HealthResponse,
  SystemStats,
  SetupResponse,
  SetupDirectoryEntry,
  SetupDirectoriesResponse,
  NetworkInterface,
  SystemMetricSample,
  HourlyStats,
  CameraUptimeStat,
  APILatencySummary,
  APIObservabilitySummary,
  APIObservabilitySeriesPoint,
  APIObservabilityRoute,
  APIObservabilityError,
  APIObservabilityResponse,
} from './client';

// Cameras — CRUD, ONVIF, PTZ, protocols
export {
  listCameras,
  createCamera,
  getCamera,
  updateCamera,
  deleteCamera,
  permanentlyDeleteCamera,
  getCameraRecordingStats,
  enableCamera,
  disableCamera,
  startCamera,
  stopCamera,
  activateCamera,
  rediscoverCamera,
  pauseRecording,
  resumeRecording,
  getDashboardCameras,
  testConnection,
  getMergeConfig,
  updateMergeConfig,
  getRecordingSchedule,
  setRecordingSchedule,
  getRecordingDays,
  deleteCameraMergeConfig,
  ptzMove,
  ptzStop,
  getPTZStatus,
  buildPTZContinuousMove,
  discoverONVIFDevices,
  getONVIFDeviceDetail,
  probeONVIFDevice,
  listProtocols,
  normalizeProtocol,
  buildProtocolsMap,
  getProtocolCapabilities,
  checkVendor,
  DEFAULT_PROTOCOLS,
  // Imaging
  getImagingSettings,
  setImagingSettings,
  getImagingOptions,
  // PTZ Presets
  getPTZPresets,
  createPTZPreset,
  goToPTZPreset,
  deletePTZPreset,
  // Snapshot URI
  getSnapshotUri,
  // Device Capabilities
  getDeviceCapabilities,
  // ONVIF Profiles
  getONVIFProfiles,
  // Device Management
  rebootDevice,
  getNetworkInterfaces,
  setNetworkInterfaces,
  getDeviceUsers,
  createDeviceUsers,
  deleteDeviceUsers,
  // Timelapse
  getTimelapseConfig,
  updateTimelapseConfig,
  // Snapshot
  getSnapshotUrl,
  getSnapshotInfo,
  takeSnapshot,
} from './cameras';
export type {
  Camera,
  CreateCameraRequest,
  UpdateCameraRequest,
  RecordingMode,
  RecordingScheduleRange,
  DiscoveredDevice,
  DiscoveryError,
  DiscoveryResult,
  DeviceInfo,
  DeviceProfile,
  ONVIFDeviceDetail,
  PTZMoveRequest,
  PTZDirection,
  PTZStatus,
  ProtocolCapabilities,
  ProtocolInfo,
  MergeConfig,
  TestConnectionRequest,
  TestConnectionResult,
  VendorCheckResult,
  PushTarget,
  // Imaging
  ImagingSettings,
  ImagingOptionRange,
  ImagingOptions,
  // PTZ Presets
  PTZPreset,
  // Snapshot URI
  SnapshotUriResponse,
  // Device Capabilities
  DeviceCapabilitiesInfo,
  PTZCapabilitiesDetailed,
  // ONVIF Profiles
  ONVIFProfilesResponse,
  // Device Management
  NetworkIPv4,
  NetworkIPv6,
  NetworkNTP,
  NetworkInterface as ONVIFNetworkInterface,
  ONVIFDeviceUser,
  // Timelapse
  TimelapseConfig,
  CameraRecordingStats,
  // Snapshot
  SnapshotInfo,
} from './cameras';
// Recordings — list, download, frames, stats, archives
export {
  listRecordings,
  getRecording,
  deleteRecording,
  batchDeleteRecordings,
  getRecordingDownloadUrl,
  downloadRecording,
  listFrames,
  loadFrameBlob,
  loadRecordingVideoBlob,
  getRecordingPlaybackUrl,
  getStats,
  getStatsTrends,
  listArchives,
  restoreArchiveGroup,
  listArchiveRecordings,
  deleteArchiveGroup,
  deleteArchiveRecording,
  setArchiveRetention,
} from './recordings';

export type {
  Recording,
  FrameInfo,
  FramesResponse,
  RecordingListResponse,
  StorageStats,
  DailyStats,
  ArchiveGroup,
  ArchiveListResponse,
} from './recordings';

// Events — unified event center
export { listEvents, getEvent, acknowledgeEvent, deleteEvent } from './events';

// Operation logs — user and system audit trail
export { listOperationLogs } from './operation-logs';
export type { OperationLog, OperationLogsResponse, OperationLogsParams } from './operation-logs';

export type { NvrEvent, EventsResponse, EventsParams, EventSource, EventSeverity, EventStatus } from './events';

// Settings — cleanup, webdav, merge, features
export {
  getSettings,
  updateSettings,
  getMergeSettings,
  updateMergeSettings,
  getMergeStatus,
  getMergePending,
  getFeatures,
  updateFeatures,
  getStreamingSettings,
  updateStreamingSettings,
  getAutoDiscoverSettings,
  updateAutoDiscoverSettings,
  getGB28181Settings,
  updateGB28181Settings,
  reloadConfig,
  checkConfigChange,
  regenerateLalmaxConfig,
  getHLSSettings,
  updateHLSSettings,
} from './settings';

export type {
  CleanupConfig,
  WebDAVConfig,
  SettingsConfig,
  MergeStatus,
  MergePending,
  FeatureFlags,
  StreamingConfig,
  WebRTCConfig,
  FLVStreamingConfig,
  RTMPConfig,
  SRTStreamConfig,
  SRTConfig,
  GB28181Config,
  HLSConfig,
  AutoDiscoverSettings,
} from './settings';

// Streams — runtime stream inventory
export {
  listStreams,
  getStream,
  bindCamera,
  unbindCamera,
  promoteStream,
  deleteStream,
  kickPublisher,
  getStreamMetricsHistory,
} from './streams';

export type {
  StreamInfo,
  StreamPlayURL,
  StreamSessionStatus,
  StreamsResponse,
  BindCameraRequest,
  PromoteStreamRequest,
  StreamOperationResponse,
  StreamMetricSample,
  StreamMetricsPeriod,
} from './streams';

// Xiaomi — cloud auth, devices, sync
export { xiaomiAuth, xiaomiDevices, xiaomiCaptcha, xiaomiVerify, xiaomiSync } from './xiaomi';

export type { XiaomiDevice, XiaomiDevicesResponse, XiaomiAuthResponse } from './xiaomi';

// Health — camera health status and events
export { getHealthStatus, getHealthEvents, getCameraHealth, getHealthCameras, getStabilityData } from './health';
export type {
  HealthStatus,
  HealthEventType,
  HealthEvent,
  CameraHealth,
  HealthStatusResponse,
  HealthEventsResponse,
  HealthEventsParams,
  CameraHealthDetail,
  HealthCamerasResponse,
  StabilityMetrics,
  StabilityDataResponse,
} from './health';

// AI Detection — localStorage-backed settings + backend API (Webhook mode only)
export {
  getAiSettings,
  saveAiSettings,
  detectAiBackend,
  getAiStatus,
  subscribeAiEvents,
  getAiBackendConfig,
  updateAiBackendConfig,
  listAiDetections,
  listAiAnalyses,
  getMultimodalStatus,
  subscribeMultimodalEvents,
} from './ai';

export type {
  AiDetectionSettings,
  AiStatusResponse,
  AiDetectionEvent,
  AiDetection,
  AiBackendConfig,
  MultimodalStatus,
  MultimodalAnalysisEvent,
  MultimodalConfig,
  MultimodalProviderConfig,
  AiDetectionHistoryResponse,
  MultimodalHistoryResponse,
} from './ai';

// GB28181 — SIP device management
export {
  listGB28181Devices,
  playGB28181Stream,
  stopGB28181Stream,
  controlGB28181PTZ,
  controlGB28181Recording,
  listGB28181Platforms,
  addGB28181Platform,
  deleteGB28181Platform,
  startBroadcast,
  stopBroadcast,
  listGB28181Alarms,
  stopDownload,
  listGB28181Downloads,
  queryDeviceRecords,
  startDevicePlayback,
  setPlaybackSpeed,
  seekPlayback,
  pausePlayback,
  resumePlayback,
  transformRecords,
  getTimelineData,
  startDownload,
  batchDownload,
  listPlatformEvents,
  getPlatformStatus,
} from './gb28181';

export type {
  GB28181Device,
  GB28181Channel,
  GB28181DevicesResponse,
  GB28181PlayRequest,
  GB28181PlayResponse,
  GB28181PTZControlRequest,
  GB28181Platform,
  GB28181PlatformsResponse,
  AddPlatformRequest,
  GB28181Alarm,
  GB28181AlarmsResponse,
  GB28181Download,
  GB28181DownloadsResponse,
  DeviceRecordItem,
  DeviceRecordResponse,
  RecordTimeSegment,
  RecordDayData,
  QueryDeviceRecordRequest,
  PlaybackRequest,
  PlaybackResponse,
  PlaySpeedRequest,
  PlaySeekRequest,
  DownloadRequest,
  DownloadResponse,
  BatchDownloadRequest,
  BatchDownloadResponse,
  PlatformEvent,
  PlatformEventsResponse,
  PlatformStatus,
  PlatformStatusResponse,
} from './gb28181';

// Groups — device grouping management
export {
  listDeviceGroups,
  getDeviceGroup,
  getDeviceGroupTree,
  createDeviceGroup,
  updateDeviceGroup,
  deleteDeviceGroup,
  listGroupChannels,
  addGroupChannel,
  removeGroupChannel,
} from './groups';

export type {
  DeviceGroup,
  DeviceGroupTreeNode,
  DeviceGroupChannelDetail,
  CreateGroupRequest,
  UpdateGroupRequest,
  AddGroupChannelRequest,
  RemoveGroupChannelRequest,
} from './groups';

// GB28181 国标目录 — 行政区划 / 业务分组
export {
  getRegionTree,
  createRegion,
  updateRegion,
  deleteRegion,
  syncRegions,
  getCivilCodeCandidates,
  getCivilCodeDescription,
  addRegionByCivilCode,
  getGroupTree,
  createGroup,
  updateGroup,
  deleteGroup,
  listCatalogChannels,
  attachChannelsToRegion,
  detachChannelsFromRegion,
  attachChannelsToGroup,
  detachChannelsFromGroup,
} from './gbCatalog';

export type { GBChannelBrief, RegionTreeNode, GroupTreeNode, ChannelKey, CivilCode } from './gbCatalog';

// Users — user management
export { listUsers, getUser, createUser, updateUser, deleteUser } from './users';

export type { User, CreateUserRequest, UpdateUserRequest } from './users';

// Relay — relay push tasks
export {
  listRelayTasks,
  getRelayTask,
  createRelayTask,
  deleteRelayTask,
  startRelayTask,
  stopRelayTask,
  getRelayTaskStats,
  getRelayTaskStatsHistory,
} from './relay';

export type { RelayTask, RelayTaskStats, StatSample, StatsHistory, CreateRelayTaskRequest } from './relay';
