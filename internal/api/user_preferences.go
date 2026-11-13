package api

import (
 "encoding/json"
 "context"
 "io"
 "net/http"
 "strings"

 "github.com/go-chi/chi/v5"
 "github.com/lalmax-pro/lalmax-nvr/internal/middleware"
 "github.com/lalmax-pro/lalmax-nvr/internal/model"
)

type accountPreferenceStore interface {
 GetUserPreference(context.Context, string, string) (string, bool, error)
 SetUserPreference(context.Context, string, string, string) error
}

func (h *Handler) userPreference(w http.ResponseWriter, r *http.Request) {
 user, ok := r.Context().Value(middleware.UserContextKey).(*model.User)
 if !ok || user == nil || user.Username == "" {
  http.Error(w, "authenticated user required", http.StatusUnauthorized)
  return
 }
 namespace := chi.URLParam(r, "namespace")
 // Restrict namespaces; preference data is private, never a global setting.
 if namespace != "dashboard" {
  http.Error(w, "unsupported preference namespace", http.StatusBadRequest)
  return
 }
 store, ok := h.db.(accountPreferenceStore)
 if !ok { http.Error(w, "user preferences unavailable", http.StatusServiceUnavailable); return }
 switch r.Method {
 case http.MethodGet:
  value, found, err := store.GetUserPreference(r.Context(), user.Username, namespace)
  if err != nil { http.Error(w, "failed to read preferences", http.StatusInternalServerError); return }
  w.Header().Set("Content-Type", "application/json")
  if !found { _, _ = io.WriteString(w, "null"); return }
  _, _ = io.WriteString(w, value)
 case http.MethodPut:
  r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
  defer r.Body.Close()
  data, err := io.ReadAll(r.Body)
  if err != nil || !json.Valid(data) || !strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
   http.Error(w, "invalid preferences JSON", http.StatusBadRequest)
   return
  }
  var dashboard struct {
   Layout int `json:"layout"`
   Slots []interface{} `json:"slots"`
  }
  if err := json.Unmarshal(data, &dashboard); err != nil || len(dashboard.Slots) != dashboard.Layout ||
   (dashboard.Layout != 1 && dashboard.Layout != 4 && dashboard.Layout != 6 && dashboard.Layout != 9 && dashboard.Layout != 16) {
   http.Error(w, "invalid dashboard layout", http.StatusBadRequest)
   return
  }
  for _, slot := range dashboard.Slots {
   if slot == nil { continue }
   id, ok := slot.(string)
   if !ok || len(id) > 256 { http.Error(w, "invalid slot", http.StatusBadRequest); return }
  }
  if err := store.SetUserPreference(r.Context(), user.Username, namespace, string(data)); err != nil {
   http.Error(w, "failed to save preferences", http.StatusInternalServerError)
   return
  }
  w.Header().Set("Content-Type", "application/json")
  _, _ = io.WriteString(w, `{"saved":true}`)
 default: w.WriteHeader(http.StatusMethodNotAllowed)
 }
}
