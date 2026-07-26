package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
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

func TestLogoutLogsOperation(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	login := doRequest(t, h.Routes(), "POST", "/api/auth/login", nil, "admin", "secret")
	require.Equal(t, http.StatusOK, login.Code)

	logout := doRequest(t, h.Routes(), "POST", "/api/auth/logout", nil, "admin", "secret")
	require.Equal(t, http.StatusOK, logout.Code)

	response := doRequest(t, h.Routes(), "GET", "/api/operation-logs?action=auth.logout", nil, "admin", "secret")
	require.Equal(t, http.StatusOK, response.Code)
	var body struct {
		Logs []struct {
			Action   string `json:"action"`
			Username string `json:"username"`
			Status   string `json:"status"`
		} `json:"logs"`
		Total int `json:"total"`
	}
	parseJSON(t, response, &body)
	require.Equal(t, 1, body.Total)
	require.Len(t, body.Logs, 1)
	require.Equal(t, "auth.logout", body.Logs[0].Action)
	require.Equal(t, "admin", body.Logs[0].Username)
	require.Equal(t, "success", body.Logs[0].Status)
}

func TestArchiveRestoreLogsOperation(t *testing.T) {
	h, _, _ := newTestCamHandler(t)

	body := strings.NewReader(`{"name":"Archive Test","protocol":"http_jpeg","url":"http://cam/snap"}`)
	create := doRequest(t, h.Routes(), "POST", "/api/cameras", body, "", "")
	require.Equal(t, http.StatusCreated, create.Code)

	var cam config.CameraConfig
	parseJSON(t, create, &cam)

	del := doRequest(t, h.Routes(), "DELETE", "/api/cameras/"+cam.ID, nil, "", "")
	require.Equal(t, http.StatusOK, del.Code)

	restore := doRequest(t, h.Routes(), "POST", "/api/archives/"+cam.ID+"/restore", nil, "", "")
	require.Equal(t, http.StatusOK, restore.Code)

	response := doRequest(t, h.Routes(), "GET", "/api/operation-logs?action=archive.restore", nil, "", "")
	require.Equal(t, http.StatusOK, response.Code)
	var logs struct {
		Logs []struct {
			Action     string `json:"action"`
			ResourceID string `json:"resource_id"`
			Username   string `json:"username"`
			Status     string `json:"status"`
		} `json:"logs"`
		Total int `json:"total"`
	}
	parseJSON(t, response, &logs)
	require.Equal(t, 1, logs.Total)
	require.Equal(t, "archive.restore", logs.Logs[0].Action)
	require.Equal(t, cam.ID, logs.Logs[0].ResourceID)
	require.Equal(t, "admin", logs.Logs[0].Username)
	require.Equal(t, "success", logs.Logs[0].Status)
}
