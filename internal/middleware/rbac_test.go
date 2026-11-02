package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

func TestHomeAssistantReadAndOperatePermissions(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	cases := []struct {
		name string
		user *model.User
		readStatus int
		operateStatus int
	}{
		{name: "ordinary reader", user: &model.User{Role: model.RoleUser, Enabled: true}, readStatus: http.StatusNoContent, operateStatus: http.StatusForbidden},
		{name: "administrator", user: &model.User{Role: model.RoleSuperAdmin, Enabled: true}, readStatus: http.StatusNoContent, operateStatus: http.StatusNoContent},
		{name: "anonymous", readStatus: http.StatusUnauthorized, operateStatus: http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.user != nil {
				req = req.WithContext(context.WithValue(req.Context(), UserContextKey, tc.user))
			}
			for _, gate := range []struct {
				name string
				middleware func(http.Handler) http.Handler
				want int
			}{
				{"read", RequireReadPermission(), tc.readStatus},
				{"operate", RequireOperatePermission(), tc.operateStatus},
			} {
				response := httptest.NewRecorder()
				gate.middleware(next).ServeHTTP(response, req)
				if response.Code != gate.want {
					t.Errorf("%s: got %d; want %d", gate.name, response.Code, gate.want)
				}
			}
		})
	}
}
