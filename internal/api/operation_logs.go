package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// logOperation records an audit entry without allowing logging failures to
// change the outcome of the operation being audited.
func (h *Handler) logOperation(r *http.Request, action, resource, resourceID, status, message string, metadata any) {
	if h.db == nil {
		return
	}
	entry := model.OperationLog{
		Action:     action,
		Resource:   resource,
		ResourceID: resourceID,
		Status:     status,
		Message:    message,
		IPAddress:  requestIP(r),
		UserAgent:  r.UserAgent(),
	}
	if user := middleware.ContextUser(r); user != nil {
		entry.UserID = user.ID
		entry.Username = user.Username
	} else {
		entry.Username = requestUsername(r)
	}
	if metadata != nil {
		if data, err := json.Marshal(metadata); err == nil {
			entry.Metadata = string(data)
		}
	}
	if _, err := h.db.InsertOperationLog(r.Context(), entry); err != nil {
		logger.Warn("failed to write operation log", "action", action, "resource", resource, "error", err)
	}
}

func requestIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func requestUsername(r *http.Request) string {
	if username, _, ok := r.BasicAuth(); ok {
		return username
	}
	if token := r.URL.Query().Get("token"); token != "" {
		if decoded, err := base64.StdEncoding.DecodeString(token); err == nil {
			if parts := strings.SplitN(string(decoded), ":", 2); len(parts) == 2 {
				return parts[0]
			}
		}
	}
	return ""
}

func (h *Handler) handleListOperationLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := storage.OperationLogsFilter{
		Username: q.Get("username"),
		Action:   q.Get("action"),
		Resource: q.Get("resource"),
		Status:   q.Get("status"),
		Since:    q.Get("since"),
		Until:    q.Get("until"),
	}
	if value := q.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 0 {
			writeError(w, http.StatusBadRequest, "invalid limit parameter")
			return
		}
		filter.Limit = limit
	}
	if value := q.Get("offset"); value != "" {
		offset, err := strconv.Atoi(value)
		if err != nil || offset < 0 {
			writeError(w, http.StatusBadRequest, "invalid offset parameter")
			return
		}
		filter.Offset = offset
	}
	logs, total, err := h.db.ListOperationLogs(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list operation logs: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"logs":   logs,
		"total":  total,
		"limit":  filter.Limit,
		"offset": filter.Offset,
	})
}
