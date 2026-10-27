package media

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPatchKeepsExplicitTranscodeIsolationListeners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lalmax.json")
	raw := `{"lal":{"rtsp":{"addr":"127.0.0.1:19255"},"default_http":{"http_listen_addr":"127.0.0.1:19280"}},"lalmax":{"http_config":{"http_listen_addr":"127.0.0.1:19291"}}}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := patchLalmaxConfig(path, true, false, ":19235", ":19200", 1, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Lal struct {
			RTSP struct {
				Addr string `json:"addr"`
			} `json:"rtsp"`
			HTTP struct {
				Addr string `json:"http_listen_addr"`
			} `json:"default_http"`
		} `json:"lal"`
		Max struct {
			HTTP struct {
				Addr string `json:"http_listen_addr"`
			} `json:"http_config"`
		} `json:"lalmax"`
	}
	if err = json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Lal.RTSP.Addr != "127.0.0.1:19255" || parsed.Lal.HTTP.Addr != "127.0.0.1:19280" || parsed.Max.HTTP.Addr != "127.0.0.1:19291" {
		t.Fatal("explicit isolated listeners overwritten")
	}
}
