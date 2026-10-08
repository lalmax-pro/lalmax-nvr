package config

import (
	"strings"
	"testing"
	"time"
)

func TestUnmarshalStructuredConfig(t *testing.T) {
	raw := []byte(`{
		"lalmax": {
			"srt_config": {
				"enable": true,
				"addr": ":6001"
			},
			"server_id": "lalmax-1"
		},
		"lal": {
			"rtmp": {
				"enable": true,
				"addr": ":1935"
			}
		}
	}`)

	if err := Unmarshal(raw); err != nil {
		t.Fatalf("unmarshal structured config: %v", err)
	}

	cfg := GetConfig()
	if !cfg.SrtConfig.Enable || cfg.SrtConfig.Addr != ":6001" {
		t.Fatalf("unexpected srt config: %+v", cfg.SrtConfig)
	}
	if cfg.JT1078Config.Enable || cfg.JT1078Config.Addr != "" {
		t.Fatalf("unexpected default jt1078 config: %+v", cfg.JT1078Config)
	}
	if cfg.ServerId != "lalmax-1" {
		t.Fatalf("unexpected server id: %s", cfg.ServerId)
	}
	if !strings.Contains(string(cfg.LalRawContent), `"rtmp"`) {
		t.Fatalf("lal raw content not preserved: %s", string(cfg.LalRawContent))
	}
}

func TestUnmarshalLegacyConfig(t *testing.T) {
	raw := []byte(`{
		"srt_config": {
			"enable": true,
			"addr": ":6001"
		},
		"httpfmp4_config": {
			"enable": true
		},
		"hls_config": {
			"enable": true,
			"segment_count": 3,
			"segment_duration": 2,
			"part_duration": 100,
			"low_latency": true
		},
		"hook_config": {
			"gop_cache_num": 3,
			"single_gop_max_frame_num": 120
		},
		"lal_config_path:": "./conf/lalserver.conf.json"
	}`)

	if err := Unmarshal(raw); err != nil {
		t.Fatalf("unmarshal legacy config: %v", err)
	}

	cfg := GetConfig()
	if !cfg.SrtConfig.Enable || cfg.SrtConfig.Addr != ":6001" {
		t.Fatalf("unexpected srt config: %+v", cfg.SrtConfig)
	}
	if cfg.LalSvrConfigPath != "./conf/lalserver.conf.json" {
		t.Fatalf("unexpected lal config path: %s", cfg.LalSvrConfigPath)
	}
	if cfg.LogicConfig.GopCacheNum != 3 || cfg.LogicConfig.SingleGopMaxFrameNum != 120 {
		t.Fatalf("unexpected legacy logic config: %+v", cfg.LogicConfig)
	}
	if !cfg.Fmp4Config.Http.Enable {
		t.Fatalf("unexpected legacy fmp4 http config: %+v", cfg.Fmp4Config.Http)
	}
	if !cfg.Fmp4Config.Hls.Enable || cfg.Fmp4Config.Hls.SegmentCount != 3 || cfg.Fmp4Config.Hls.SegmentDuration != 2 || cfg.Fmp4Config.Hls.PartDuration != 100 || !cfg.Fmp4Config.Hls.LowLatency {
		t.Fatalf("unexpected legacy fmp4 hls config: %+v", cfg.Fmp4Config.Hls)
	}
	if len(cfg.LalRawContent) != 0 {
		t.Fatalf("legacy config should not set lal raw content: %s", string(cfg.LalRawContent))
	}
}

func TestUnmarshalJT1078Config(t *testing.T) {
	raw := []byte(`{
		"lalmax": {
			"jt1078_config": {
				"enable": true,
				"addr": ":1078"
			}
		}
	}`)
	if err := Unmarshal(raw); err != nil {
		t.Fatalf("unmarshal jt1078 config: %v", err)
	}
	cfg := GetConfig()
	if !cfg.JT1078Config.Enable || cfg.JT1078Config.Addr != ":1078" {
		t.Fatalf("unexpected jt1078 config: %+v", cfg.JT1078Config)
	}
	if !cfg.JT1078Config.UDPEnabled() || cfg.JT1078Config.UDPListenAddr() != ":1078" {
		t.Fatalf("udp should follow enable when udp_enable is omitted: %+v", cfg.JT1078Config)
	}
}

func TestJT1078UDPConfigOverrides(t *testing.T) {
	off := false
	cfg := JT1078Config{
		Enable:           true,
		Addr:             ":1078",
		UDPEnable:        &off,
		UDPAddr:          ":2078",
		UDPIdleTimeoutMs: 3000,
	}
	if cfg.UDPEnabled() {
		t.Fatal("explicit udp_enable=false should win")
	}
	if cfg.UDPListenAddr() != ":2078" {
		t.Fatalf("udp addr=%s", cfg.UDPListenAddr())
	}
	if cfg.UDPIdleTimeout() != 3*time.Second {
		t.Fatalf("idle=%s", cfg.UDPIdleTimeout())
	}
}

func TestUnmarshalStructuredFmp4ConfigKeepsExplicitZero(t *testing.T) {
	raw := []byte(`{
		"lalmax": {
			"fmp4_config": {
				"http": {
					"enable": false
				},
				"hls": {
					"enable": true,
					"segment_count": 0,
					"segment_duration": 0,
					"part_duration": 0,
					"low_latency": false
				}
			},
			"httpfmp4_config": {
				"enable": true
			},
			"hls_config": {
				"enable": true,
				"segment_count": 3,
				"segment_duration": 2,
				"part_duration": 100,
				"low_latency": true
			}
		}
	}`)

	if err := Unmarshal(raw); err != nil {
		t.Fatalf("unmarshal structured config: %v", err)
	}

	cfg := GetConfig()
	if cfg.Fmp4Config.Http.Enable {
		t.Fatalf("explicit fmp4 http config should not be overwritten: %+v", cfg.Fmp4Config.Http)
	}
	if !cfg.Fmp4Config.Hls.Enable || cfg.Fmp4Config.Hls.SegmentCount != 0 || cfg.Fmp4Config.Hls.SegmentDuration != 0 || cfg.Fmp4Config.Hls.PartDuration != 0 || cfg.Fmp4Config.Hls.LowLatency {
		t.Fatalf("explicit fmp4 hls config should not be overwritten: %+v", cfg.Fmp4Config.Hls)
	}
}

func TestUnmarshalStructuredLogicConfigKeepsExplicitZero(t *testing.T) {
	raw := []byte(`{
		"lalmax": {
			"logic_config": {
				"gop_cache_num": 0,
				"single_gop_max_frame_num": 0
			},
			"hook_config": {
				"gop_cache_num": 3,
				"single_gop_max_frame_num": 120
			}
		}
	}`)

	if err := Unmarshal(raw); err != nil {
		t.Fatalf("unmarshal structured config: %v", err)
	}

	cfg := GetConfig()
	if cfg.LogicConfig.GopCacheNum != 0 || cfg.LogicConfig.SingleGopMaxFrameNum != 0 {
		t.Fatalf("explicit logic config should not be overwritten: %+v", cfg.LogicConfig)
	}
}
