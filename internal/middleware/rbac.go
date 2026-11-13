package middleware

import (
	"context"
	"net/http"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

type contextKey string

const UserContextKey contextKey = "user"

func ContextUser(r *http.Request) *model.User {
	u, _ := r.Context().Value(UserContextKey).(*model.User)
	return u
}

func RequireRole(roles ...model.Role) func(http.Handler) http.Handler {
	roleSet := make(map[model.Role]bool, len(roles))
	for _, r := range roles {
		roleSet[r] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := ContextUser(r)
			if user == nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			if !roleSet[user.Role] {
				http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func RequireOperatePermission() func(http.Handler) http.Handler {
	return RequireRole(model.RoleSuperAdmin)
}

// RequireReadPermission permits authenticated NVR users to consume published
// Home Assistant media, without granting publication, settings, or PTZ writes.
func RequireReadPermission() func(http.Handler) http.Handler {
	return RequireRole(model.RoleSuperAdmin, model.RoleUser)
}

func InjectUser(user *model.User) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), UserContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
