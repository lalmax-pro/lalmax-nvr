package storage

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// HomeAssistantPublication keeps identity and application even while a publisher is offline.
type HomeAssistantPublication struct {
	TranscodeJobID string `json:"transcode_job_id,omitempty"`
	StreamID       string `json:"stream_id"`
	Name           string `json:"name,omitempty"`
	AppName        string `json:"app_name"`
}

type HomeAssistantSettings struct {
	InstanceID  string                     `json:"instance_id"`
	Enabled     bool                       `json:"enabled"`
	RTSPBaseURL string                     `json:"rtsp_base_url"`
	Streams     []HomeAssistantPublication `json:"streams"`
}

func (d *DB) createHomeAssistantTable(ctx context.Context) error {
	_, err := d.execContext(ctx, `CREATE TABLE IF NOT EXISTS home_assistant_settings (
		id TEXT PRIMARY KEY, instance_id TEXT NOT NULL, enabled INTEGER NOT NULL,
		rtsp_base_url TEXT NOT NULL, streams TEXT NOT NULL
	)`)
	if err != nil {
		return err
	}
	_, err = d.execContext(ctx, `INSERT OR IGNORE INTO home_assistant_settings
		(id, instance_id, enabled, rtsp_base_url, streams) VALUES (?, ?, ?, ?, ?)`,
		"main", uuid.NewString(), false, "", "[]")
	return err
}

func (d *DB) GetHomeAssistantSettings(ctx context.Context) (HomeAssistantSettings, error) {
	var cfg HomeAssistantSettings
	var streams string
	err := d.queryRowContext(ctx, `SELECT instance_id, enabled, rtsp_base_url, streams
		FROM home_assistant_settings WHERE id = ?`, "main").Scan(&cfg.InstanceID, &cfg.Enabled, &cfg.RTSPBaseURL, &streams)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal([]byte(streams), &cfg.Streams)
	if cfg.Streams == nil {
		cfg.Streams = []HomeAssistantPublication{}
	}
	return cfg, err
}

// SaveHomeAssistantSettings never changes the database's stable instance identity.
func (d *DB) SaveHomeAssistantSettings(ctx context.Context, cfg HomeAssistantSettings) error {
	if cfg.Streams == nil {
		cfg.Streams = []HomeAssistantPublication{}
	}
	streams, err := json.Marshal(cfg.Streams)
	if err != nil {
		return err
	}
	_, err = d.execContext(ctx, `UPDATE home_assistant_settings SET enabled = ?, rtsp_base_url = ?, streams = ? WHERE id = ?`,
		cfg.Enabled, cfg.RTSPBaseURL, string(streams), "main")
	return err
}
