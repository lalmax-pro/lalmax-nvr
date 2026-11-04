// SPDX-License-Identifier: MIT

package xiaomi

import (
	"context"
	"errors"
	"time"
)

var errMotorSpeed = errors.New("motor speed must be between 1 and 100")
var errMotorDirection = errors.New("invalid motor direction")

func (r *XiaomiRecorder) attachMotorClient(client *MISSClient) {
	r.motorMu.Lock()
	defer r.motorMu.Unlock()
	r.invalidateMotorTimer()
	r.motorClient = client
	r.motorActive = false
}

func (r *XiaomiRecorder) detachMotorClient(client *MISSClient) {
	r.motorMu.Lock()
	defer r.motorMu.Unlock()
	if r.motorClient != client {
		return
	}
	r.stopMotorLocked()
	r.motorClient = nil
}

// invalidateMotorTimer runs under motorMu. A generation check prevents an old
// timeout from stopping a new movement or a reconnected camera session.
func (r *XiaomiRecorder) invalidateMotorTimer() {
	r.motorGeneration++
	if r.motorTimer != nil {
		r.motorTimer.Stop()
		r.motorTimer = nil
	}
}

func (r *XiaomiRecorder) armMotorStop(delay time.Duration) {
	generation, client := r.motorGeneration, r.motorClient
	r.motorTimer = time.AfterFunc(delay, func() {
		r.motorMu.Lock()
		defer r.motorMu.Unlock()
		if r.motorGeneration != generation || r.motorClient != client || !r.motorActive {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := client.MotorControlContext(ctx, "stop", 0); err != nil {
			xiaomiLogger.Warn("automatic motor stop failed", "camera_id", r.cfg.CameraID, "error", err)
			r.armMotorStop(500 * time.Millisecond)
			return
		}
		r.motorActive = false
	})
}

// MotorControl uses the recording session, so controls do not open another
// device connection. Movements expire unless the caller renews them.
func (r *XiaomiRecorder) MotorControl(ctx context.Context, direction string, speed int) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r.motorMu.Lock()
	defer r.motorMu.Unlock()
	if r.motorClient == nil {
		return ErrNotConnected
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Validate before altering the current movement's expiry.
	switch direction {
	case "left", "right", "up", "down":
		if speed < 1 || speed > 100 {
			return errMotorSpeed
		}
	case "stop":
		speed = 0
	default:
		return errMotorDirection
	}
	r.invalidateMotorTimer()
	r.motorActive = true
	lease := r.motorLease
	if lease <= 0 {
		lease = 2 * time.Second
	}
	r.armMotorStop(lease)
	err := r.motorClient.MotorControlContext(ctx, direction, speed)
	if direction == "stop" && err == nil {
		r.motorActive = false
		r.invalidateMotorTimer()
	}
	return err
}

func (r *XiaomiRecorder) stopMotorBeforeClose() {
	r.motorMu.Lock()
	defer r.motorMu.Unlock()
	r.stopMotorLocked()
}

func (r *XiaomiRecorder) stopMotorLocked() {
	r.invalidateMotorTimer()
	if r.motorClient != nil && r.motorActive {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := r.motorClient.MotorControlContext(ctx, "stop", 0); err != nil {
			xiaomiLogger.Warn("motor stop before disconnect failed", "camera_id", r.cfg.CameraID, "error", err)
		}
	}
	r.motorActive = false
}

type MotorProfile struct {
	Model          string `json:"model"`
	Control        string `json:"control"`
	Source         string `json:"source"`
	DeviceVerified bool   `json:"device_verified"`
}

func MotorProfileForModel(model string) MotorProfile {
	profile := MotorProfile{Model: model, Control: "unknown", Source: "unknown"}
	switch model {
	case ModelCW400, ModelCW500:
		profile.Control = "fixed_step"
		profile.Source = "model"
	}
	return profile
}

func (r *XiaomiRecorder) MotorModel() string {
	r.motorMu.Lock()
	defer r.motorMu.Unlock()
	if r.motorClient == nil {
		return ""
	}
	return r.motorClient.model
}

func (r *XiaomiRecorder) MotorProfile() MotorProfile {
	r.motorMu.Lock()
	defer r.motorMu.Unlock()
	if r.motorClient == nil {
		return MotorProfileForModel("")
	}
	profile := MotorProfileForModel(r.motorClient.model)
	if r.motorClient.model == ModelDafang && !r.motorClient.legacy {
		profile.Control, profile.Source = "continuous", "model"
	}
	return profile
}
