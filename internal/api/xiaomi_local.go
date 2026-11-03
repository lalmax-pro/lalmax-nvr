package api

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi"
)

// LocalXiaomiAuth implements CloudAuthProxy by calling the internal xiaomi package directly.
type LocalXiaomiAuth struct {
	mu  sync.RWMutex
	cfg config.XiaomiConfig
}

// NewLocalXiaomiAuth creates a LocalXiaomiAuth and initializes cloud config.
// The credential snapshot is updated through SetCloudConfig.
func NewLocalXiaomiAuth(cfg *config.Config) *LocalXiaomiAuth {
	// Initialize cloud config for MISS URL resolution (was previously called
	// from main.go as xiaomi.SetCloudConfig(cfg.Xiaomi)).
	a := &LocalXiaomiAuth{}
	if cfg != nil {
		a.cfg = cfg.Xiaomi
	}
	xiaomi.SetCloudConfig(a.cfg)
	return a
}

// SetCloudConfig pushes credentials to the xiaomi package's cloud state.
func (a *LocalXiaomiAuth) SetCloudConfig(ctx context.Context, userID, token, region string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg = config.XiaomiConfig{
		UserID: userID,
		Token:  token,
		Region: region,
	}
	xiaomi.SetCloudConfig(a.cfg)
	if token == "" {
		xiaomi.ClearPendingLogins()
	}
	return nil
}

func (a *LocalXiaomiAuth) cloudConfig() config.XiaomiConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

// SignIn calls the xiaomi cloud sign-in flow.
func (a *LocalXiaomiAuth) SignIn(ctx context.Context, username, password, region string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	session, captchaSessionID, err := xiaomi.SignInWithCaptchaContext(ctx, username, password, region)
	if err != nil {
		var loginErr *xiaomi.LoginError
		if isLoginErr(err, &loginErr) {
			return nil, loginErrToVerification(loginErr, captchaSessionID), nil
		}
		return nil, nil, err
	}
	return sessionToResult(session), nil, nil
}

// SubmitCaptcha submits a captcha code for a pending auth session.
func (a *LocalXiaomiAuth) SubmitCaptcha(ctx context.Context, sessionID, captchaCode string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	session, err := xiaomi.LoginWithCaptchaContext(ctx, sessionID, captchaCode)
	if err != nil {
		var captchaErr *xiaomi.CaptchaSessionError
		if isCaptchaErr(err, &captchaErr) {
			return nil, captchaErrToVerification(captchaErr), nil
		}
		return nil, nil, err
	}
	return sessionToResult(session), nil, nil
}

// SubmitVerify submits a 2FA ticket for a pending auth session.
func (a *LocalXiaomiAuth) SubmitVerify(ctx context.Context, sessionID, ticket string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	session, err := xiaomi.LoginWithVerifyContext(ctx, sessionID, ticket)
	if err != nil {
		var captchaErr *xiaomi.CaptchaSessionError
		if isCaptchaErr(err, &captchaErr) {
			return nil, captchaErrToVerification(captchaErr), nil
		}
		return nil, nil, err
	}
	return sessionToResult(session), nil, nil
}

// ListDevices returns Xiaomi cloud devices filtered to camera models.
func (a *LocalXiaomiAuth) ListDevices(ctx context.Context) ([]CloudDeviceInfo, error) {
	cfg := a.cloudConfig()
	session, err := xiaomi.SignInWithTokenContext(ctx, cfg.UserID, cfg.Token, cfg.Region)
	if err != nil {
		return nil, err
	}

	devices, err := xiaomi.GetDeviceListContext(ctx, session)
	if err != nil {
		return nil, err
	}

	// Filter for camera devices only
	cameras := make([]CloudDeviceInfo, 0, len(devices))
	for _, d := range devices {
		if isXiaomiCameraModel(d.Model) {
			cameras = append(cameras, CloudDeviceInfo{
				DID:      d.DID,
				Name:     d.Name,
				Model:    d.Model,
				IP:       d.IP,
				MAC:      d.MAC,
				IsOnline: d.IsOnline,
			})
		}
	}
	return cameras, nil
}

// CheckVendor checks the vendor protocol for a Xiaomi device by DID.
func (a *LocalXiaomiAuth) CheckVendor(ctx context.Context, did string) (string, error) {
	cfg := a.cloudConfig()
	if cfg.UserID == "" || cfg.Token == "" {
		return "", fmt.Errorf("xiaomi config not available")
	}
	cloudCfg := xiaomi.XiaomiCloudConfig{
		UserID: cfg.UserID,
		Token:  cfg.Token,
		Region: cfg.Region,
	}
	missURL, err := xiaomi.ResolveMISSURLContext(ctx, cloudCfg, did, "")
	if err != nil {
		return "", err
	}
	// Parse the MISS URL to extract the vendor query parameter
	u, err := url.Parse(missURL)
	if err != nil {
		return "", err
	}
	vendor := u.Query().Get("vendor")
	if vendor == "" {
		return "cs2", nil // default to cs2 if vendor not specified
	}
	return vendor, nil
}

// --- helpers ---

func sessionToResult(s *xiaomi.CloudSession) *CloudAuthResult {
	if s == nil {
		return nil
	}
	return &CloudAuthResult{
		UserID:    s.UserID,
		PassToken: s.PassToken,
		Region:    s.Region,
	}
}

func loginErrToVerification(e *xiaomi.LoginError, sessionID string) *CloudVerificationRequired {
	return &CloudVerificationRequired{
		Captcha:          e.Captcha,
		VerifyPhone:      e.VerifyPhone,
		VerifyEmail:      e.VerifyEmail,
		CaptchaSessionID: sessionID,
	}
}

func captchaErrToVerification(e *xiaomi.CaptchaSessionError) *CloudVerificationRequired {
	return &CloudVerificationRequired{
		Captcha:          e.Captcha,
		VerifyPhone:      e.VerifyPhone,
		VerifyEmail:      e.VerifyEmail,
		CaptchaSessionID: e.CaptchaSessionID,
	}
}

func isLoginErr(err error, target **xiaomi.LoginError) bool {
	return errors.As(err, target)
}

func isCaptchaErr(err error, target **xiaomi.CaptchaSessionError) bool {
	return errors.As(err, target)
}

// isXiaomiCameraModel returns true if the model string looks like a Xiaomi camera.
// Matches camera, doorbell and feeder model families.
func isXiaomiCameraModel(model string) bool {
	return strings.Contains(model, ".camera.") ||
		strings.Contains(model, ".cateye.") ||
		strings.Contains(model, ".feeder.")
}
