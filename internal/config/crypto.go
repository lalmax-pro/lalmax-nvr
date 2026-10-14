package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const encPrefix = "ENC:"

var (
	ErrEncryptionKeyConfigured = errors.New("encryption key is already configured or managed by the environment")
	ErrInvalidEncryptionKey    = errors.New("encryption key must be exactly 32 bytes, either raw or base64-encoded")
)

// encryptionKey caches the active key for the process lifetime. The web UI can
// configure it once in a permission-restricted sidecar file.
var encryptionKey []byte
var encryptionKeyMu sync.RWMutex
var webEncryptionKeyFile string

// SetWebEncryptionKeyFile configures the sidecar path used by web-managed keys.
// Call it before loading configuration so encrypted values can be decrypted.
func SetWebEncryptionKeyFile(path string) {
	encryptionKeyMu.Lock()
	webEncryptionKeyFile = path
	encryptionKey = nil
	encryptionKeyMu.Unlock()
}

// EncryptionKeySource reports where the active encryption key is managed.
// The key itself is never returned.
func EncryptionKeySource() string {
	if strings.TrimSpace(os.Getenv("NVR_ENCRYPTION_KEY")) != "" {
		return "environment"
	}
	if strings.TrimSpace(os.Getenv("NVR_ENCRYPTION_KEY_FILE")) != "" {
		return "environment_file"
	}
	encryptionKeyMu.RLock()
	path := webEncryptionKeyFile
	encryptionKeyMu.RUnlock()
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			return "web"
		}
	}
	return "none"
}

// CreateWebEncryptionKey persists a key configured through the web UI. It is
// intentionally set-once: replacing a key would make existing ciphertext
// unreadable without a full data migration.
func CreateWebEncryptionKey(path, keyValue string) error {
	key, err := parseEncryptionKey(keyValue)
	if err != nil {
		return ErrInvalidEncryptionKey
	}
	if strings.TrimSpace(os.Getenv("NVR_ENCRYPTION_KEY")) != "" || strings.TrimSpace(os.Getenv("NVR_ENCRYPTION_KEY_FILE")) != "" {
		return ErrEncryptionKeyConfigured
	}
	if path == "" {
		return errors.New("config path is not available")
	}

	encryptionKeyMu.Lock()
	defer encryptionKeyMu.Unlock()
	if encryptionKey != nil {
		return ErrEncryptionKeyConfigured
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrEncryptionKeyConfigured
		}
		return fmt.Errorf("create encryption key file: %w", err)
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("write encryption key file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("close encryption key file: %w", err)
	}
	encryptionKey = key
	webEncryptionKeyFile = path
	return nil
}

// GetEncryptionKey returns the AES-256 encryption key from NVR_ENCRYPTION_KEY
// environment variable. The key must be exactly 32 bytes (base64-encoded or raw).
// Returns nil if no key is configured.
func GetEncryptionKey() []byte {
	encryptionKeyMu.RLock()
	if encryptionKey != nil {
		key := encryptionKey
		encryptionKeyMu.RUnlock()
		return key
	}
	encryptionKeyMu.RUnlock()

	keyStr := os.Getenv("NVR_ENCRYPTION_KEY")
	if keyStr != "" {
		key, err := parseEncryptionKey(keyStr)
		if err != nil {
			return nil
		}
		encryptionKeyMu.Lock()
		if encryptionKey == nil {
			encryptionKey = key
		}
		key = encryptionKey
		encryptionKeyMu.Unlock()
		return key
	}

	if keyStr == "" {
		// Also check for key file
		keyFile := os.Getenv("NVR_ENCRYPTION_KEY_FILE")
		if keyFile != "" {
			data, err := os.ReadFile(keyFile)
			if err != nil {
				return nil
			}
			keyStr = strings.TrimSpace(string(data))
		}
	}
	if keyStr == "" {
		encryptionKeyMu.RLock()
		keyFile := webEncryptionKeyFile
		encryptionKeyMu.RUnlock()
		if keyFile != "" {
			data, err := os.ReadFile(keyFile)
			if err == nil {
				keyStr = strings.TrimSpace(string(data))
			}
		}
	}
	if keyStr == "" {
		return nil
	}

	key, err := parseEncryptionKey(keyStr)
	if err != nil {
		return nil
	}
	encryptionKeyMu.Lock()
	if encryptionKey == nil {
		encryptionKey = key
	}
	key = encryptionKey
	encryptionKeyMu.Unlock()
	return key
}

func parseEncryptionKey(keyStr string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyStr))
	if err == nil && len(key) == 32 {
		return key, nil
	}
	if len(keyStr) == 32 {
		return []byte(keyStr), nil
	}
	return nil, errors.New("encryption key must be exactly 32 bytes, either raw or base64-encoded")
}

// HasKey returns true if an encryption key is available.
func HasKey() bool {
	return GetEncryptionKey() != nil
}

// Encrypt encrypts plaintext using AES-256-GCM and returns a string
// in the format "ENC:base64(nonce+ciphertext+tag)".
// The key must be exactly 32 bytes.
func Encrypt(plaintext string, key []byte) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
	}
	if plaintext == "" {
		return "", nil
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	// Seal appends ciphertext+tag to nonce
	ciphertext := aesGCM.Seal(nonce, nonce, []byte(plaintext), nil)

	return encPrefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a value that was encrypted with Encrypt.
// If the value does not start with "ENC:", it is returned as-is (plaintext passthrough).
func Decrypt(encoded string, key []byte) (string, error) {
	if !strings.HasPrefix(encoded, encPrefix) {
		return encoded, nil
	}

	if len(key) != 32 {
		return "", fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
	}

	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, encPrefix))
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := aesGCM.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}

// IsEncrypted returns true if the value starts with the ENC: prefix.
func IsEncrypted(value string) bool {
	return strings.HasPrefix(value, encPrefix)
}

// decryptConfig decrypts all sensitive fields in the config in place.
// Fields without the ENC: prefix are left unchanged.
func decryptConfig(cfg *Config, key []byte) {
	if key == nil {
		return
	}
	if v, err := Decrypt(cfg.Storage.DatabaseDSN, key); err == nil {
		cfg.Storage.DatabaseDSN = v
	}
	if cfg.Storage.MigrationTarget != nil {
		if v, err := Decrypt(cfg.Storage.MigrationTarget.DSN, key); err == nil {
			cfg.Storage.MigrationTarget.DSN = v
		}
	}
	// Auth password (plaintext, before auto-hash conversion)
	if v, err := Decrypt(cfg.Auth.Password, key); err == nil {
		cfg.Auth.Password = v
	} // if decrypt fails, leave as-is (might be plaintext)
	for streamID, token := range cfg.Auth.PlaybackTokens {
		if v, err := Decrypt(token, key); err == nil {
			cfg.Auth.PlaybackTokens[streamID] = v
		}
	}
	if v, err := Decrypt(cfg.Media.RTSPPassword, key); err == nil {
		cfg.Media.RTSPPassword = v
	}

	// MQTT password
	if v, err := Decrypt(cfg.MQTT.Password, key); err == nil {
		cfg.MQTT.Password = v
	}
	// Xiaomi credentials
	if v, err := Decrypt(cfg.Xiaomi.UserID, key); err == nil {
		cfg.Xiaomi.UserID = v
	}
	if v, err := Decrypt(cfg.Xiaomi.Token, key); err == nil {
		cfg.Xiaomi.Token = v
	}
	if v, err := Decrypt(cfg.GB28181.Password, key); err == nil {
		cfg.GB28181.Password = v
	}

	// Camera passwords
	for i := range cfg.Cameras {
		if v, err := Decrypt(cfg.Cameras[i].Password, key); err == nil {
			cfg.Cameras[i].Password = v
		}
	}
	for streamID, token := range cfg.RTMP.StreamKeys {
		if v, err := Decrypt(token, key); err == nil {
			cfg.RTMP.StreamKeys[streamID] = v
		}
	}
	for streamID, token := range cfg.SRT.StreamKeys {
		if v, err := Decrypt(token, key); err == nil {
			cfg.SRT.StreamKeys[streamID] = v
		}
	}
	for streamID, token := range cfg.WHIP.PublishTokens {
		if v, err := Decrypt(token, key); err == nil {
			cfg.WHIP.PublishTokens[streamID] = v
		}
	}
	for i := range cfg.VoIP.Users {
		if v, err := Decrypt(cfg.VoIP.Users[i].Password, key); err == nil {
			cfg.VoIP.Users[i].Password = v
		}
	}
	// Metrics auth password
	if v, err := Decrypt(cfg.MetricsAuth.Password, key); err == nil {
		cfg.MetricsAuth.Password = v
	}
	if v, err := Decrypt(cfg.AutoDiscover.DefaultPassword, key); err == nil {
		cfg.AutoDiscover.DefaultPassword = v
	}
}

// encryptConfig encrypts all sensitive fields in the config in place.
// Returns a list of field names that were encrypted.
// Callers should restore plaintext values after saving.
func encryptConfig(cfg *Config, key []byte) []string {
	if key == nil {
		return nil
	}

	var encrypted []string
	if cfg.Storage.DatabaseDSN != "" && !IsEncrypted(cfg.Storage.DatabaseDSN) {
		if v, err := Encrypt(cfg.Storage.DatabaseDSN, key); err == nil {
			cfg.Storage.DatabaseDSN = v
			encrypted = append(encrypted, "storage.database_dsn")
		}
	}
	if cfg.Storage.MigrationTarget != nil && cfg.Storage.MigrationTarget.DSN != "" && !IsEncrypted(cfg.Storage.MigrationTarget.DSN) {
		if v, err := Encrypt(cfg.Storage.MigrationTarget.DSN, key); err == nil {
			cfg.Storage.MigrationTarget.DSN = v
			encrypted = append(encrypted, "storage.database_migration_target.dsn")
		}
	}

	// Auth password (skip if empty or already encrypted)
	if cfg.Auth.Password != "" && !IsEncrypted(cfg.Auth.Password) {
		if v, err := Encrypt(cfg.Auth.Password, key); err == nil {
			cfg.Auth.Password = v
			encrypted = append(encrypted, "auth.password")
		}
	}
	if cfg.Media.RTSPPassword != "" && !IsEncrypted(cfg.Media.RTSPPassword) {
		if v, err := Encrypt(cfg.Media.RTSPPassword, key); err == nil {
			cfg.Media.RTSPPassword = v
			encrypted = append(encrypted, "media.rtsp_password")
		}
	}
	for streamID, token := range cfg.Auth.PlaybackTokens {
		if token != "" && !IsEncrypted(token) {
			if v, err := Encrypt(token, key); err == nil {
				cfg.Auth.PlaybackTokens[streamID] = v
				encrypted = append(encrypted, fmt.Sprintf("auth.playback_tokens[%s]", streamID))
			}
		}
	}

	// MQTT password
	if cfg.MQTT.Password != "" && !IsEncrypted(cfg.MQTT.Password) {
		if v, err := Encrypt(cfg.MQTT.Password, key); err == nil {
			cfg.MQTT.Password = v
			encrypted = append(encrypted, "mqtt.password")
		}
	}

	// Xiaomi credentials
	if cfg.Xiaomi.UserID != "" && !IsEncrypted(cfg.Xiaomi.UserID) {
		if v, err := Encrypt(cfg.Xiaomi.UserID, key); err == nil {
			cfg.Xiaomi.UserID = v
			encrypted = append(encrypted, "xiaomi.user_id")
		}
	}
	if cfg.Xiaomi.Token != "" && !IsEncrypted(cfg.Xiaomi.Token) {
		if v, err := Encrypt(cfg.Xiaomi.Token, key); err == nil {
			cfg.Xiaomi.Token = v
			encrypted = append(encrypted, "xiaomi.token")
		}
	}
	if cfg.GB28181.Password != "" && !IsEncrypted(cfg.GB28181.Password) {
		if v, err := Encrypt(cfg.GB28181.Password, key); err == nil {
			cfg.GB28181.Password = v
			encrypted = append(encrypted, "gb28181.password")
		}
	}

	// Camera passwords
	for i := range cfg.Cameras {
		if cfg.Cameras[i].Password != "" && !IsEncrypted(cfg.Cameras[i].Password) {
			if v, err := Encrypt(cfg.Cameras[i].Password, key); err == nil {
				cfg.Cameras[i].Password = v
				encrypted = append(encrypted, fmt.Sprintf("cameras[%d].password", i))
			}
		}
	}
	for streamID, token := range cfg.RTMP.StreamKeys {
		if token != "" && !IsEncrypted(token) {
			if v, err := Encrypt(token, key); err == nil {
				cfg.RTMP.StreamKeys[streamID] = v
				encrypted = append(encrypted, fmt.Sprintf("rtmp.stream_keys[%s]", streamID))
			}
		}
	}
	for streamID, token := range cfg.SRT.StreamKeys {
		if token != "" && !IsEncrypted(token) {
			if v, err := Encrypt(token, key); err == nil {
				cfg.SRT.StreamKeys[streamID] = v
				encrypted = append(encrypted, fmt.Sprintf("srt.stream_keys[%s]", streamID))
			}
		}
	}
	for streamID, token := range cfg.WHIP.PublishTokens {
		if token != "" && !IsEncrypted(token) {
			if v, err := Encrypt(token, key); err == nil {
				cfg.WHIP.PublishTokens[streamID] = v
				encrypted = append(encrypted, fmt.Sprintf("whip.publish_tokens[%s]", streamID))
			}
		}
	}
	for i := range cfg.VoIP.Users {
		if password := cfg.VoIP.Users[i].Password; password != "" && !IsEncrypted(password) {
			if v, err := Encrypt(password, key); err == nil {
				cfg.VoIP.Users[i].Password = v
				encrypted = append(encrypted, fmt.Sprintf("voip.users[%d].password", i))
			}
		}
	}
	// Metrics auth password
	if cfg.MetricsAuth.Password != "" && !IsEncrypted(cfg.MetricsAuth.Password) {
		if v, err := Encrypt(cfg.MetricsAuth.Password, key); err == nil {
			cfg.MetricsAuth.Password = v
			encrypted = append(encrypted, "metrics_auth.password")
		}
	}
	if cfg.AutoDiscover.DefaultPassword != "" && !IsEncrypted(cfg.AutoDiscover.DefaultPassword) {
		if v, err := Encrypt(cfg.AutoDiscover.DefaultPassword, key); err == nil {
			cfg.AutoDiscover.DefaultPassword = v
			encrypted = append(encrypted, "auto_discover.default_password")
		}
	}

	return encrypted
}

// SensitiveFieldPaths returns a list of field paths for sensitive values
// currently stored in plaintext (not encrypted).
func SensitiveFieldPaths(cfg *Config) []string {
	var fields []string
	if cfg.Storage.DatabaseDSN != "" && !IsEncrypted(cfg.Storage.DatabaseDSN) {
		fields = append(fields, "storage.database_dsn")
	}

	if cfg.Auth.Password != "" && !IsEncrypted(cfg.Auth.Password) {
		fields = append(fields, "auth.password")
	}
	if cfg.Media.RTSPPassword != "" && !IsEncrypted(cfg.Media.RTSPPassword) {
		fields = append(fields, "media.rtsp_password")
	}
	if cfg.MQTT.Password != "" && !IsEncrypted(cfg.MQTT.Password) {
		fields = append(fields, "mqtt.password")
	}
	if cfg.Xiaomi.UserID != "" && !IsEncrypted(cfg.Xiaomi.UserID) {
		fields = append(fields, "xiaomi.user_id")
	}
	if cfg.Xiaomi.Token != "" && !IsEncrypted(cfg.Xiaomi.Token) {
		fields = append(fields, "xiaomi.token")
	}
	if cfg.GB28181.Password != "" && !IsEncrypted(cfg.GB28181.Password) {
		fields = append(fields, "gb28181.password")
	}
	for i := range cfg.Cameras {
		if cfg.Cameras[i].Password != "" && !IsEncrypted(cfg.Cameras[i].Password) {
			fields = append(fields, fmt.Sprintf("cameras[%d].password", i))
		}
	}
	for streamID, token := range cfg.RTMP.StreamKeys {
		if token != "" && !IsEncrypted(token) {
			fields = append(fields, fmt.Sprintf("rtmp.stream_keys[%s]", streamID))
		}
	}
	for streamID, token := range cfg.SRT.StreamKeys {
		if token != "" && !IsEncrypted(token) {
			fields = append(fields, fmt.Sprintf("srt.stream_keys[%s]", streamID))
		}
	}
	for streamID, token := range cfg.WHIP.PublishTokens {
		if token != "" && !IsEncrypted(token) {
			fields = append(fields, fmt.Sprintf("whip.publish_tokens[%s]", streamID))
		}
	}
	if cfg.MetricsAuth.Password != "" && !IsEncrypted(cfg.MetricsAuth.Password) {
		fields = append(fields, "metrics_auth.password")
	}
	if cfg.AutoDiscover.DefaultPassword != "" && !IsEncrypted(cfg.AutoDiscover.DefaultPassword) {
		fields = append(fields, "auto_discover.default_password")
	}
	if cfg.Storage.MigrationTarget != nil && cfg.Storage.MigrationTarget.DSN != "" && !IsEncrypted(cfg.Storage.MigrationTarget.DSN) {
		fields = append(fields, "storage.database_migration_target.dsn")
	}

	for i, u := range cfg.VoIP.Users {
		if u.Password != "" && !IsEncrypted(u.Password) {
			fields = append(fields, fmt.Sprintf("voip.users[%d].password", i))
		}
	}
	for streamID, token := range cfg.Auth.PlaybackTokens {
		if token != "" && !IsEncrypted(token) {
			fields = append(fields, fmt.Sprintf("auth.playback_tokens[%s]", streamID))
		}
	}

	return fields
}

// snapshotSensitive captures the current plaintext values of sensitive fields
// so they can be restored after an encrypted save.
type sensitiveSnapshot struct {
	VoIPPasswords        []string
	RTMPStreamKeys       map[string]string
	SRTStreamKeys        map[string]string
	WHIPPublishTokens    map[string]string
	AuthPassword         string
	AuthPlaybackTokens   map[string]string
	RTSPPassword         string
	MQTTPassword         string
	XiaomiUserID         string
	XiaomiToken          string
	GB28181Password      string
	CameraPasswords      []string
	MetricsAuthPassword  string
	AutoDiscoverPassword string
}

func snapshotSensitive(cfg *Config) sensitiveSnapshot {
	s := sensitiveSnapshot{
		RTMPStreamKeys:       cloneStringMap(cfg.RTMP.StreamKeys),
		SRTStreamKeys:        cloneStringMap(cfg.SRT.StreamKeys),
		WHIPPublishTokens:    cloneStringMap(cfg.WHIP.PublishTokens),
		AuthPassword:         cfg.Auth.Password,
		AuthPlaybackTokens:   cloneStringMap(cfg.Auth.PlaybackTokens),
		RTSPPassword:         cfg.Media.RTSPPassword,
		MQTTPassword:         cfg.MQTT.Password,
		XiaomiUserID:         cfg.Xiaomi.UserID,
		XiaomiToken:          cfg.Xiaomi.Token,
		GB28181Password:      cfg.GB28181.Password,
		CameraPasswords:      make([]string, len(cfg.Cameras)),
		VoIPPasswords:        make([]string, len(cfg.VoIP.Users)),
		MetricsAuthPassword:  cfg.MetricsAuth.Password,
		AutoDiscoverPassword: cfg.AutoDiscover.DefaultPassword,
	}
	for i := range cfg.Cameras {
		s.CameraPasswords[i] = cfg.Cameras[i].Password
	}
	for i, u := range cfg.VoIP.Users {
		s.VoIPPasswords[i] = u.Password
	}
	return s
}

func (s sensitiveSnapshot) restore(cfg *Config) {
	for i := range cfg.VoIP.Users {
		if i < len(s.VoIPPasswords) {
			cfg.VoIP.Users[i].Password = s.VoIPPasswords[i]
		}
	}
	cfg.Auth.Password = s.AuthPassword
	cfg.Auth.PlaybackTokens = cloneStringMap(s.AuthPlaybackTokens)
	cfg.Media.RTSPPassword = s.RTSPPassword
	cfg.RTMP.StreamKeys = cloneStringMap(s.RTMPStreamKeys)
	cfg.SRT.StreamKeys = cloneStringMap(s.SRTStreamKeys)
	cfg.WHIP.PublishTokens = cloneStringMap(s.WHIPPublishTokens)
	cfg.MQTT.Password = s.MQTTPassword
	cfg.Xiaomi.UserID = s.XiaomiUserID
	cfg.Xiaomi.Token = s.XiaomiToken
	cfg.GB28181.Password = s.GB28181Password
	for i := range cfg.Cameras {
		if i < len(s.CameraPasswords) {
			cfg.Cameras[i].Password = s.CameraPasswords[i]
		}
	}
	cfg.MetricsAuth.Password = s.MetricsAuthPassword
	cfg.AutoDiscover.DefaultPassword = s.AutoDiscoverPassword
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
