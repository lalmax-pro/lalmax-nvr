package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOperationLogsEndpointIncludesOperationTime(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	login := doRequest(t, h.Routes(), "POST", "/api/auth/login", nil, "admin", "secret")
	require.Equal(t, http.StatusOK, login.Code)

	response := doRequest(t, h.Routes(), "GET", "/api/operation-logs?action=auth.login", nil, "admin", "secret")
	require.Equal(t, http.StatusOK, response.Code)
	var body struct {
		Logs []struct {
			Action    string `json:"action"`
			CreatedAt string `json:"created_at"`
		} `json:"logs"`
		Total int `json:"total"`
	}
	parseJSON(t, response, &body)
	require.Equal(t, 1, body.Total)
	require.Len(t, body.Logs, 1)
	require.Equal(t, "auth.login", body.Logs[0].Action)
	require.NotEmpty(t, body.Logs[0].CreatedAt)
}
