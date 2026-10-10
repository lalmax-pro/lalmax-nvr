package config

import (
	"sync"
	"testing"
)

func TestConfigSnapshotsSurviveConcurrentReload(t *testing.T) {
	if err := Unmarshal([]byte(`{"server_id":"original","rtc_config":{"ice_host_nat_to_ips":["127.0.0.1"]}}`)); err != nil {
		t.Fatal(err)
	}
	old := GetConfig()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				if err := Unmarshal([]byte(`{"server_id":"replacement"}`)); err != nil {
					t.Error(err)
				}
				GetConfig().ServerId = "caller-change"
			}
		}()
	}
	for n := 0; n < 100; n++ {
		if old.ServerId != "original" || old.RtcConfig.ICEHostNATToIPs[0] != "127.0.0.1" {
			t.Fatal("reload mutated active configuration")
		}
	}
	wg.Wait()
	if GetConfig().ServerId != "replacement" {
		t.Fatal("caller mutation leaked into global config")
	}
}
