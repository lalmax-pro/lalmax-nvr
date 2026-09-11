package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/ai"
	"github.com/lalmax-pro/lalmax-nvr/internal/ai/multimodal"
)

type AIHistoryFilter struct {
	CameraID string
	Label    string
	Limit    int
	Offset   int
}

func (d *DB) createAITables(ctx context.Context) error {
	detectionSQL := `CREATE TABLE IF NOT EXISTS ai_detections (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		camera_id TEXT NOT NULL,
		pts INTEGER DEFAULT 0,
		timestamp INTEGER NOT NULL,
		image_url TEXT DEFAULT '',
		detections_json TEXT NOT NULL DEFAULT '[]',
		created_at TEXT DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
	);`
	analysisSQL := `CREATE TABLE IF NOT EXISTS ai_analyses (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		camera_id TEXT NOT NULL,
		timestamp INTEGER NOT NULL,
		analysis TEXT NOT NULL DEFAULT '',
		labels_json TEXT NOT NULL DEFAULT '[]',
		confidence REAL DEFAULT 0,
		image_url TEXT DEFAULT '',
		trigger_detections_json TEXT NOT NULL DEFAULT '[]',
		metadata_json TEXT NOT NULL DEFAULT '{}',
		created_at TEXT DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
	);`
	if _, err := d.db.ExecContext(ctx, detectionSQL); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, analysisSQL); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_ai_detections_camera_time ON ai_detections(camera_id, timestamp DESC)`)
	_, _ = d.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_ai_analyses_camera_time ON ai_analyses(camera_id, timestamp DESC)`)
	return nil
}

func (d *DB) InsertAIDetection(ctx context.Context, result ai.DetectionResult) error {
	if result.Timestamp == 0 {
		result.Timestamp = time.Now().UnixMilli()
	}
	payload, err := json.Marshal(result.Detections)
	if err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, `
		INSERT INTO ai_detections (camera_id, pts, timestamp, image_url, detections_json)
		VALUES (?, ?, ?, ?, ?)`,
		result.CameraID, result.PTS, result.Timestamp, result.ImageURL, string(payload),
	)
	return err
}

func (d *DB) InsertAIAnalysis(ctx context.Context, raw interface{}) error {
	result, ok := raw.(multimodal.AnalysisResult)
	if !ok {
		if ptr, ok := raw.(*multimodal.AnalysisResult); ok && ptr != nil {
			result = *ptr
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("unsupported AI analysis type %T", raw)
	}
	if result.Timestamp == 0 {
		result.Timestamp = time.Now().UnixMilli()
	}
	labels, err := json.Marshal(result.Labels)
	if err != nil {
		return err
	}
	triggers, err := json.Marshal(result.TriggerDetections)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(result.Metadata)
	if err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, `
		INSERT INTO ai_analyses (camera_id, timestamp, analysis, labels_json, confidence, image_url, trigger_detections_json, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		result.CameraID, result.Timestamp, result.Analysis, string(labels), result.Confidence, result.ImageURL, string(triggers), string(metadata),
	)
	return err
}

func (d *DB) ListAIDetections(ctx context.Context, filter AIHistoryFilter) ([]ai.DetectionResult, int, error) {
	limit, offset := normalizeAIHistoryPage(filter.Limit, filter.Offset)
	where, args := aiHistoryWhere(filter.CameraID, filter.Label)

	var total int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_detections `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := d.db.QueryContext(ctx, `
		SELECT camera_id, pts, timestamp, image_url, detections_json
		FROM ai_detections `+where+`
		ORDER BY timestamp DESC, id DESC
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []ai.DetectionResult
	for rows.Next() {
		var result ai.DetectionResult
		var payload string
		if err := rows.Scan(&result.CameraID, &result.PTS, &result.Timestamp, &result.ImageURL, &payload); err != nil {
			return nil, 0, err
		}
		_ = json.Unmarshal([]byte(payload), &result.Detections)
		out = append(out, result)
	}
	return out, total, rows.Err()
}

func (d *DB) ListAIAnalyses(ctx context.Context, filter AIHistoryFilter) ([]multimodal.AnalysisResult, int, error) {
	limit, offset := normalizeAIHistoryPage(filter.Limit, filter.Offset)
	where, args := aiHistoryWhere(filter.CameraID, "")

	var total int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_analyses `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := d.db.QueryContext(ctx, `
		SELECT camera_id, timestamp, analysis, labels_json, confidence, image_url, trigger_detections_json, metadata_json
		FROM ai_analyses `+where+`
		ORDER BY timestamp DESC, id DESC
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []multimodal.AnalysisResult
	for rows.Next() {
		var result multimodal.AnalysisResult
		var labels, triggers, metadata string
		if err := rows.Scan(&result.CameraID, &result.Timestamp, &result.Analysis, &labels, &result.Confidence, &result.ImageURL, &triggers, &metadata); err != nil {
			return nil, 0, err
		}
		_ = json.Unmarshal([]byte(labels), &result.Labels)
		_ = json.Unmarshal([]byte(triggers), &result.TriggerDetections)
		_ = json.Unmarshal([]byte(metadata), &result.Metadata)
		out = append(out, result)
	}
	return out, total, rows.Err()
}

func normalizeAIHistoryPage(limit, offset int) (int, int) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func aiHistoryWhere(cameraID, label string) (string, []interface{}) {
	where := []string{}
	args := []interface{}{}
	if cameraID != "" {
		where = append(where, "camera_id = ?")
		args = append(args, cameraID)
	}
	if label != "" {
		where = append(where, "detections_json LIKE ?")
		args = append(args, "%"+label+"%")
	}
	if len(where) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(where, " AND "), args
}
