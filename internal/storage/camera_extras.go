package storage

import (
	"encoding/json"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
)

// cameraExtras holds per-camera settings that are not stored in dedicated DB columns.
type cameraExtras struct {
	Timelapse            *config.CameraTimelapseConfig `json:"timelapse,omitempty"`
	HealthOverrides      config.HealthOverrides        `json:"health_overrides,omitempty"`
	AudioEnabled         *bool                         `json:"audio_enabled,omitempty"`
	SubStreamURL         string                        `json:"sub_stream_url,omitempty"`
	SnapshotURL          string                        `json:"snapshot_url,omitempty"`
	SampleInterval       int                           `json:"sample_interval,omitempty"`
	HLSMaxFPS            int                           `json:"hls_max_fps,omitempty"`
	FrameWatchdogTimeout string                        `json:"frame_watchdog_timeout,omitempty"`
	PullRetryNum         int                           `json:"pull_retry_num,omitempty"`
	DID                  string                        `json:"did,omitempty"`
	Vendor               string                        `json:"vendor,omitempty"`
	SourceType           string                        `json:"source_type,omitempty"`
	SubProfileToken      string                        `json:"sub_profile_token,omitempty"`
	SubnetHints          []string                      `json:"subnet_hints,omitempty"`
	Adaptive             *config.CameraAdaptiveConfig  `json:"adaptive,omitempty"`
}

func extrasFromCameraConfig(cam config.CameraConfig) cameraExtras {
	extras := cameraExtras{
		Timelapse:            cam.Timelapse,
		HealthOverrides:      cam.HealthOverrides,
		SubStreamURL:         cam.SubStreamURL,
		SnapshotURL:          cam.SnapshotURL,
		SampleInterval:       cam.SampleInterval,
		HLSMaxFPS:            cam.HLSMaxFPS,
		FrameWatchdogTimeout: cam.FrameWatchdogTimeout,
		PullRetryNum:         cam.PullRetryNum,
		DID:                  cam.DID,
		Vendor:               cam.Vendor,
		SourceType:           cam.SourceType,
		SubProfileToken:      cam.SubProfileToken,
		SubnetHints:          cam.SubnetHints,
		Adaptive:             cam.Adaptive,
	}
	if config.CameraSupportsAudioRecording(cam) {
		v := cam.AudioEnabled
		extras.AudioEnabled = &v
	}
	return extras
}

func applyExtrasToCamera(cam *config.CameraConfig, extras cameraExtras) {
	cam.Timelapse = extras.Timelapse
	cam.HealthOverrides = extras.HealthOverrides
	if extras.AudioEnabled != nil {
		cam.AudioEnabled = *extras.AudioEnabled
	}
	cam.SubStreamURL = extras.SubStreamURL
	cam.SnapshotURL = extras.SnapshotURL
	cam.SampleInterval = extras.SampleInterval
	cam.HLSMaxFPS = extras.HLSMaxFPS
	cam.FrameWatchdogTimeout = extras.FrameWatchdogTimeout
	cam.PullRetryNum = extras.PullRetryNum
	cam.DID = extras.DID
	cam.Vendor = extras.Vendor
	cam.SourceType = extras.SourceType
	cam.SubProfileToken = extras.SubProfileToken
	cam.SubnetHints = extras.SubnetHints
	cam.Adaptive = extras.Adaptive
}

func mergeConfigFromRow(row CameraRow) *config.MergeConfig {
	if row.MergeEnabled == nil && row.MergeCheckInterval == nil && row.MergeWindowSize == nil &&
		row.MergeBatchLimit == nil && row.MergeMinSegmentAge == nil && row.MergeMinSegmentsToMerge == nil {
		return nil
	}
	mergeCfg := &config.MergeConfig{}
	if row.MergeEnabled != nil {
		mergeCfg.Enabled = *row.MergeEnabled
	}
	if row.MergeCheckInterval != nil {
		mergeCfg.CheckInterval = *row.MergeCheckInterval
	}
	if row.MergeWindowSize != nil {
		mergeCfg.WindowSize = *row.MergeWindowSize
	}
	if row.MergeBatchLimit != nil {
		mergeCfg.BatchLimit = *row.MergeBatchLimit
	}
	if row.MergeMinSegmentAge != nil {
		mergeCfg.MinSegmentAge = *row.MergeMinSegmentAge
	}
	if row.MergeMinSegmentsToMerge != nil {
		mergeCfg.MinSegmentsToMerge = *row.MergeMinSegmentsToMerge
	}
	return mergeCfg
}

func marshalCameraExtras(cam config.CameraConfig) (string, error) {
	data, err := json.Marshal(extrasFromCameraConfig(cam))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalCameraExtras(raw string) (cameraExtras, error) {
	if raw == "" {
		return cameraExtras{}, nil
	}
	var extras cameraExtras
	if err := json.Unmarshal([]byte(raw), &extras); err != nil {
		return cameraExtras{}, err
	}
	return extras, nil
}

func extrasHasAudioEnabled(raw string) bool {
	if raw == "" {
		return false
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return false
	}
	_, ok := meta["audio_enabled"]
	return ok
}
