package api

import (
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"net/http"
	"strings"
	"testing"
)

func TestJT808ControlRequiresOperatePermission(t *testing.T) {
	h := setupJT808Handler(t, &stubJT808{})
	h.SetMultiUserAuthMW(middleware.InjectUser(&model.User{Role: model.RoleUser}))
	router := h.Routes()
	for _, endpoint := range []string{"play", "stop", "control", "loss", "playback", "playback/control", "resources", "upload", "upload/control", "ptz", "downlink"} {
		t.Run(endpoint, func(t *testing.T) {
			resp := doRequest(t, router, "POST", "/api/jt808/"+endpoint, strings.NewReader(`{"key":"1003"}`), "", "")
			if resp.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
			}
		})
	}
	resp := doRequest(t, router, "GET", "/api/jt808/terminals", nil, "", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("terminal list status=%d", resp.Code)
	}
}
