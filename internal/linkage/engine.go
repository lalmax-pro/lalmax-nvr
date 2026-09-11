package linkage

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

var logger = slog.Default().With("component", "linkage")

type RecorderTrigger interface {
	HandleActivity(cameraID, source string)
}

type PresetGoto interface {
	GoToPreset(ctx context.Context, cameraID, token string) error
}

type Engine struct {
	db       *storage.DB
	recorder RecorderTrigger
	ptz      PresetGoto
	client   *http.Client
}

func New(db *storage.DB, recorder RecorderTrigger, ptz PresetGoto) *Engine {
	return &Engine{
		db:       db,
		recorder: recorder,
		ptz:      ptz,
		client:   &http.Client{Timeout: 8 * time.Second},
	}
}

func (e *Engine) Dispatch(ev model.Event) {
	if e == nil || e.db == nil {
		return
	}
	rules, err := e.db.ListAlarmRules(context.Background())
	if err != nil {
		logger.Warn("list alarm rules failed", "error", err)
		return
	}
	for _, rule := range rules {
		if !match(rule, ev) {
			continue
		}
		e.run(rule, ev)
	}
}

func match(rule model.AlarmRule, ev model.Event) bool {
	if !rule.Enabled {
		return false
	}
	if rule.CameraID != "" && rule.CameraID != ev.CameraID {
		return false
	}
	if rule.Source != "" && rule.Source != ev.Source {
		return false
	}
	if rule.EventType != "" && rule.EventType != ev.Type {
		return false
	}
	if rule.Severity != "" && rule.Severity != ev.Severity {
		return false
	}
	return true
}

func (e *Engine) run(rule model.AlarmRule, ev model.Event) {
	switch rule.Action {
	case model.AlarmActionRecord:
		if e.recorder != nil && ev.CameraID != "" {
			e.recorder.HandleActivity(ev.CameraID, "linkage")
		}
	case model.AlarmActionWebhook:
		e.postWebhook(rule.ActionTarget, ev)
	case model.AlarmActionGotoPreset:
		if e.ptz != nil && ev.CameraID != "" && rule.ActionTarget != "" {
			if err := e.ptz.GoToPreset(context.Background(), ev.CameraID, rule.ActionTarget); err != nil {
				logger.Warn("goto preset failed", "camera_id", ev.CameraID, "preset", rule.ActionTarget, "error", err)
			}
		}
	default:
		logger.Debug("unknown alarm action", "action", rule.Action)
	}
}

func (e *Engine) postWebhook(target string, ev model.Event) {
	target = strings.TrimSpace(target)
	if target == "" || e.client == nil {
		return
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		logger.Warn("webhook request build failed", "url", target, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		logger.Warn("webhook post failed", "url", target, "error", err)
		return
	}
	resp.Body.Close()
}
