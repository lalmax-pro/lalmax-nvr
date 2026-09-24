package iptv

import (
	"encoding/json"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
)

func sealSecret(plain string) string {
	if plain == "" {
		return ""
	}
	key := config.GetEncryptionKey()
	if key == nil {
		return plain
	}
	enc, err := config.Encrypt(plain, key)
	if err != nil {
		return plain
	}
	return enc
}

func openSecret(stored string) string {
	if stored == "" {
		return ""
	}
	key := config.GetEncryptionKey()
	if key == nil {
		return stored
	}
	plain, err := config.Decrypt(stored, key)
	if err != nil {
		return stored
	}
	return plain
}

func sealHeaders(headers map[string]string) string {
	clean := sanitizeHeaders(headers)
	if len(clean) == 0 {
		return ""
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return ""
	}
	return sealSecret(string(raw))
}

func openHeaders(stored string) map[string]string {
	plain := openSecret(stored)
	if plain == "" {
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil
	}
	return sanitizeHeaders(out)
}

func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	if len(raw) <= 24 {
		return raw[:min(8, len(raw))] + "…"
	}
	return raw[:18] + "…" + raw[len(raw)-6:]
}
