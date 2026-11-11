package api

import (
	"net/http"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestXiaomiVendorCheckDoesNotMisreportOtherTransportsAsCS2(t *testing.T) {
	t.Helper()
	for _, vendor := range []string{"agora", "mtp", "42"} {
		t.Run(vendor, func(t *testing.T) {
			t.Helper()
			cloud := &api2ioCloud{vendor: vendor}
			cfg := &config.Config{Xiaomi: config.XiaomiConfig{UserID: "user", Token: "token", Region: "cn"}}
			h, _, _, _ := api2ioNewHandler(t, cfg, cloud)
			rr := api2ioDo(t, h, http.MethodGet, "/api/xiaomi/check-vendor?did=1", "")
			require.Equal(t, http.StatusOK, rr.Code)
			var result struct {
				Vendor     string `json:"vendor"`
				Compatible bool   `json:"compatible"`
				Message    string `json:"message"`
			}
			parseJSON(t, rr, &result)
			require.Equal(t, vendor, result.Vendor)
			require.False(t, result.Compatible)
			require.Contains(t, result.Message, vendor)
		})
	}
}
