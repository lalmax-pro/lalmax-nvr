package main

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/lalmax-pro/lalmax-nvr/internal/voip"
	maxvoip "github.com/q191201771/lalmax/voip"
)

// RestartVoIP serializes Web changes with shutdown. Failed listener changes
// restore the previous runtime; a successful change ends any active calls.
func (a *App) RestartVoIP(ctx context.Context, cfg *voip.Config) error {
	a.voipMu.Lock()
	defer a.voipMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.startCtx != nil && a.startCtx.Err() != nil {
		return fmt.Errorf("VoIP is shutting down")
	}
	if a.voipStopped {
		return fmt.Errorf("VoIP is shutting down")
	}
	next := cfg.Clone()
	next.Normalize()
	if next.Enable {
		if err := next.Validate(); err != nil {
			return err
		}
		if _, ok := a.mediaEngine.(*media.EmbeddedLalmax); !ok {
			return fmt.Errorf("VoIP requires embedded media")
		}
	}
	if reflect.DeepEqual(a.voipApplied, next) && (a.voipSvr != nil || !next.Enable) {
		return nil
	}
	old := a.voipApplied.Clone()
	if a.voipSvr != nil {
		a.voipSvr.Close()
		a.voipSvr = nil
	}
	create := func(c voip.Config) (*voip.Server, error) {
		if !c.Enable {
			return nil, nil
		}
		publisher := media.NewVoIPPublisher(a.mediaEngine)
		record := func(session *maxvoip.PubSession) {
			for _, u := range c.Users {
				if u.Username == session.FromUser() && u.RecordCalls && a.groupWriter != nil {
					a.groupWriter.AttachVoIP(session.StreamName(), session.Negotiated().Video == nil)
					return
				}
			}
		}
		embedded := a.mediaEngine.(*media.EmbeddedLalmax)
		return voip.NewServer(voip.ServerConfig{Config: c, NewTalkSession: embedded.Server().NewTalkSession,
			OnCallEnded: func(call voip.CallRecord) {
				if a.db == nil {
					return
				}
				err := a.db.SaveVoIPCall(context.Background(), storage.VoIPCall{
					CallID: call.CallID, Direction: call.Direction, FromUser: call.FromUser, ToUser: call.ToUser,
					Outcome: call.Outcome, StartedAt: call.StartedAt, AnsweredAt: call.AnsweredAt, EndedAt: call.EndedAt,
					DurationSecond: call.DurationSecond, FailureReason: call.FailureReason, RemoteAddr: call.RemoteAddr,
					Transport: string(call.Transport), AudioCodec: call.AudioCodec, VideoCodec: call.VideoCodec, StreamID: call.StreamID,
				})
				if err != nil {
					slog.Error("failed to save VoIP call history", "call_id", call.CallID, "error", err)
				}
			},
			OnPubSession: func(session *maxvoip.PubSession) error {
				if err := publisher.Add(session); err != nil {
					return err
				}
				record(session)
				return nil
			},
			OnReplaceSession: func(old, next *maxvoip.PubSession) error {
				if err := publisher.Replace(old, next); err != nil {
					return err
				}
				record(next)
				return nil
			},
			OnDelSession: func(session *maxvoip.PubSession) error {
				if a.groupWriter != nil {
					a.groupWriter.DetachVoIP(session.StreamName())
				}
				return publisher.Remove(session)
			},
		})
	}
	server, err := create(next)
	if err != nil {
		restored, restoreErr := create(old)
		a.voipSvr = restored
		if restoreErr != nil {
			return fmt.Errorf("VoIP update failed: %v; restore failed: %w", err, restoreErr)
		}
		return err
	}
	a.voipSvr = server
	a.voipApplied = next
	return nil
}

func (a *App) stopVoIP() {
	a.voipMu.Lock()
	defer a.voipMu.Unlock()
	a.voipStopped = true
	if a.voipSvr != nil {
		a.voipSvr.Close()
		a.voipSvr = nil
	}
}

func (a *App) VoIPStatus() voip.Status {
	a.voipMu.Lock()
	defer a.voipMu.Unlock()
	if a.voipSvr != nil {
		return a.voipSvr.Status()
	}
	return voip.Status{Endpoints: []voip.EndpointStatus{}, Calls: []voip.CallStatus{}}
}
func (a *App) HangupVoIP(id string) bool {
	a.voipMu.Lock()
	defer a.voipMu.Unlock()
	return a.voipSvr != nil && a.voipSvr.Hangup(id)
}

func (a *App) AnswerVoIPCall(id string) error {
	a.voipMu.Lock()
	server := a.voipSvr
	a.voipMu.Unlock()
	if server == nil {
		return fmt.Errorf("VoIP is disabled")
	}
	return server.AnswerIncomingCall(id)
}

func (a *App) RejectVoIPCall(id string) error {
	a.voipMu.Lock()
	server := a.voipSvr
	a.voipMu.Unlock()
	if server == nil {
		return fmt.Errorf("VoIP is disabled")
	}
	return server.RejectIncomingCall(id)
}

func (a *App) DialVoIP(user, security, offer string) (voip.DialResult, error) {
	a.voipMu.Lock()
	defer a.voipMu.Unlock()
	if a.voipSvr == nil {
		return voip.DialResult{}, fmt.Errorf("VoIP is disabled")
	}
	return a.voipSvr.Dial(user, security, offer)
}
func (a *App) AttachVoIPTalk(ctx context.Context, id, token, offer string) (string, error) {
	a.voipMu.Lock()
	server := a.voipSvr
	a.voipMu.Unlock()
	if server == nil {
		return "", fmt.Errorf("VoIP is disabled")
	}
	return server.AttachTalk(ctx, id, token, offer)
}
func (a *App) KeepVoIPTalk(id, token string) bool {
	a.voipMu.Lock()
	defer a.voipMu.Unlock()
	return a.voipSvr != nil && a.voipSvr.KeepTalk(id, token)
}

func (a *App) ClaimVoIPTalk(id string) (string, error) {
	a.voipMu.Lock()
	defer a.voipMu.Unlock()
	if a.voipSvr == nil {
		return "", fmt.Errorf("VoIP is disabled")
	}
	return a.voipSvr.ClaimInboundTalk(id)
}

func (a *App) DetachVoIPTalk(id, token string) bool {
	a.voipMu.Lock()
	server := a.voipSvr
	a.voipMu.Unlock()
	return server != nil && server.DetachInboundTalk(id, token)
}

func (a *App) SendVoIPDTMF(id, digit string) error {
	a.voipMu.Lock()
	server := a.voipSvr
	a.voipMu.Unlock()
	if server == nil {
		return fmt.Errorf("VoIP is disabled")
	}
	return server.SendDTMF(id, digit)
}
