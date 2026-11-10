package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSFUICEServerValidationAndSecretSerialization(t *testing.T) {
	valid := SFUConfig{ICEServers: []SFUICEServer{{URLs: []string{"stun:example.com:3478", "turn:relay.example.com:3478?transport=udp"}, Username: "user", Credential: "private-secret"}}}
	if err := valid.ValidateICE(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(valid)
	if err != nil || strings.Contains(string(encoded), "private-secret") {
		t.Fatalf("configuration exposed secret: %s %v", encoded, err)
	}
	for _, server := range []SFUICEServer{
		{}, {URLs: []string{"https://example.com"}}, {URLs: []string{"turn:relay.example.com"}},
		{URLs: []string{"turn:user:secret@relay.example.com"}, Username: "user", Credential: "secret"},
	} {
		if err := (SFUConfig{ICEServers: []SFUICEServer{server}}).ValidateICE(); err == nil {
			t.Fatalf("accepted invalid server: %+v", server)
		}
	}
}
