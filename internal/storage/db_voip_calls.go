package storage

import (
	"context"
	"database/sql"
	"time"
)

// VoIPCall is a persisted summary of a completed SIP call.
type VoIPCall struct {
	CallID         string    `json:"call_id"`
	Direction      string    `json:"direction"`
	FromUser       string    `json:"from_user"`
	ToUser         string    `json:"to_user"`
	Outcome        string    `json:"outcome"`
	StartedAt      time.Time `json:"started_at"`
	AnsweredAt     time.Time `json:"answered_at,omitempty"`
	EndedAt        time.Time `json:"ended_at"`
	DurationSecond int64     `json:"duration_seconds"`
	FailureReason  string    `json:"failure_reason,omitempty"`
	RemoteAddr     string    `json:"remote_addr,omitempty"`
	Transport      string    `json:"transport,omitempty"`
	AudioCodec     string    `json:"audio_codec,omitempty"`
	VideoCodec     string    `json:"video_codec,omitempty"`
	StreamID       string    `json:"stream_id,omitempty"`
}

func (d *DB) createVoIPCallTable(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS voip_calls (
		call_id TEXT PRIMARY KEY,
		direction TEXT NOT NULL,
		from_user TEXT NOT NULL DEFAULT '',
		to_user TEXT NOT NULL DEFAULT '',
		outcome TEXT NOT NULL,
		started_at DATETIME NOT NULL,
		answered_at DATETIME,
		ended_at DATETIME NOT NULL,
		duration_seconds INTEGER NOT NULL DEFAULT 0,
		failure_reason TEXT NOT NULL DEFAULT '',
		remote_addr TEXT NOT NULL DEFAULT '',
		transport TEXT NOT NULL DEFAULT '',
		audio_codec TEXT NOT NULL DEFAULT '',
		video_codec TEXT NOT NULL DEFAULT '',
		stream_id TEXT NOT NULL DEFAULT ''
	); CREATE INDEX IF NOT EXISTS idx_voip_calls_ended ON voip_calls(ended_at DESC);`)
	return err
}

func (d *DB) SaveVoIPCall(ctx context.Context, call VoIPCall) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO voip_calls
		(call_id,direction,from_user,to_user,outcome,started_at,answered_at,ended_at,duration_seconds,failure_reason,remote_addr,transport,audio_codec,video_codec,stream_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(call_id) DO UPDATE SET
		direction=excluded.direction,from_user=excluded.from_user,to_user=excluded.to_user,outcome=excluded.outcome,
		started_at=excluded.started_at,answered_at=excluded.answered_at,ended_at=excluded.ended_at,
		duration_seconds=excluded.duration_seconds,failure_reason=excluded.failure_reason,remote_addr=excluded.remote_addr,
		transport=excluded.transport,audio_codec=excluded.audio_codec,video_codec=excluded.video_codec,stream_id=excluded.stream_id;`,
		call.CallID, call.Direction, call.FromUser, call.ToUser, call.Outcome,
		timeToDB(call.StartedAt), nullableTimeToDB(call.AnsweredAt), timeToDB(call.EndedAt), call.DurationSecond,
		call.FailureReason, call.RemoteAddr, call.Transport, call.AudioCodec, call.VideoCodec, call.StreamID)
	return err
}

func (d *DB) ListVoIPCalls(ctx context.Context, limit, offset int) ([]VoIPCall, int, error) {
	var total int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM voip_calls`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.db.QueryContext(ctx, `SELECT call_id,direction,from_user,to_user,outcome,started_at,answered_at,ended_at,
		duration_seconds,failure_reason,remote_addr,transport,audio_codec,video_codec,stream_id
		FROM voip_calls ORDER BY ended_at DESC, call_id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	calls := make([]VoIPCall, 0)
	for rows.Next() {
		var call VoIPCall
		var started, answered, ended sql.NullString
		if err := rows.Scan(&call.CallID, &call.Direction, &call.FromUser, &call.ToUser, &call.Outcome,
			&started, &answered, &ended, &call.DurationSecond, &call.FailureReason, &call.RemoteAddr,
			&call.Transport, &call.AudioCodec, &call.VideoCodec, &call.StreamID); err != nil {
			return nil, 0, err
		}
		call.StartedAt = scanTime(started)
		call.AnsweredAt = scanTime(answered)
		call.EndedAt = scanTime(ended)
		calls = append(calls, call)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return calls, total, nil
}

func nullableTimeToDB(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return timeToDB(value)
}
