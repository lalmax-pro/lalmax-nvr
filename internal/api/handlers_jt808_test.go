package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/jt808"
	"github.com/stretchr/testify/require"
)

type stubJT808 struct {
	play    jt808.PlayInput
	stop    jt808.StopPlayInput
	control jt808.LiveControlInput
	err     error
}

func (s *stubJT808) ListTerminals() []jt808.Terminal {
	return []jt808.Terminal{{Key: "1003", Online: true}}
}
func (s *stubJT808) Play(in jt808.PlayInput) (string, error) {
	s.play = in
	return "1003_1", s.err
}
func (s *stubJT808) StopPlay(in jt808.StopPlayInput) error {
	s.stop = in
	return s.err
}
func (s *stubJT808) LiveControl(in jt808.LiveControlInput) error {
	s.control = in
	return s.err
}
func (s *stubJT808) ReportLoss(jt808.LossInput) error { return s.err }
func (s *stubJT808) QueryAV(string) (jt808.AVProperties, error) {
	return jt808.AVProperties{VideoCodec: 98}, s.err
}
func (s *stubJT808) Playback(jt808.PlaybackInput) (string, error) { return "1003_1", s.err }
func (s *stubJT808) PlaybackControl(jt808.PlaybackControlInput) error {
	return s.err
}
func (s *stubJT808) QueryResources(jt808.ResourceQuery) ([]jt808.Resource, error) {
	return nil, s.err
}
func (s *stubJT808) Upload(jt808.UploadInput) (uint16, error) { return 7, s.err }
func (s *stubJT808) UploadControl(jt808.UploadControlInput) error {
	return s.err
}
func (s *stubJT808) PTZ(jt808.PTZInput) error { return s.err }

func setupJT808Handler(t *testing.T, svc jt808Signaling) *Handler {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	h.SetJT808Server(svc)
	return h
}

func TestJT808APIDisabledAndValidation(t *testing.T) {
	h := setupJT808Handler(t, nil)
	resp := doRequest(t, h.Routes(), "GET", "/api/jt808/terminals", nil, "", "")
	require.Equal(t, http.StatusOK, resp.Code)
	resp = doRequest(t, h.Routes(), "POST", "/api/jt808/play", strings.NewReader(`{"key":"1003"}`), "", "")
	require.Equal(t, http.StatusServiceUnavailable, resp.Code)
	h.SetJT808Server(&stubJT808{})
	resp = doRequest(t, h.Routes(), "POST", "/api/jt808/play", strings.NewReader(`{"key":""}`), "", "")
	require.Equal(t, http.StatusBadRequest, resp.Code)
	resp = doRequest(t, h.Routes(), "POST", "/api/jt808/stop", strings.NewReader(`{`), "", "")
	require.Equal(t, http.StatusBadRequest, resp.Code)
}

func TestJT808APIPlayStopAndError(t *testing.T) {
	svc := &stubJT808{}
	h := setupJT808Handler(t, svc)
	resp := doRequest(t, h.Routes(), "GET", "/api/jt808/terminals", nil, "", "")
	require.Equal(t, http.StatusOK, resp.Code)
	resp = doRequest(t, h.Routes(), "POST", "/api/jt808/play", strings.NewReader(`{"key":"1003","channel":1,"transport":"udp","data_type":2,"stream":1}`), "", "")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, "udp", svc.play.Transport)
	require.Equal(t, byte(1), svc.play.Channel)
	require.Equal(t, byte(2), svc.play.DataType)
	require.Equal(t, byte(1), svc.play.Stream)
	resp = doRequest(t, h.Routes(), "POST", "/api/jt808/control", strings.NewReader(`{"key":"1003","channel":1,"control":2}`), "", "")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, byte(2), svc.control.Control)
	resp = doRequest(t, h.Routes(), "POST", "/api/jt808/downlink", strings.NewReader(`{"key":"1003","payload":"9Q=="}`), "", "")
	require.Equal(t, http.StatusServiceUnavailable, resp.Code)
	resp = doRequest(t, h.Routes(), "POST", "/api/jt808/stop", strings.NewReader(`{"key":"1003","channel":1}`), "", "")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, byte(1), svc.stop.Channel)
	svc.err = errors.New("terminal offline")
	resp = doRequest(t, h.Routes(), "POST", "/api/jt808/play", strings.NewReader(`{"key":"1003"}`), "", "")
	require.Equal(t, http.StatusInternalServerError, resp.Code)
	svc.err = jt808.ErrBadInput
	resp = doRequest(t, h.Routes(), "POST", "/api/jt808/ptz", strings.NewReader(`{"key":"1003","action":"spin"}`), "", "")
	require.Equal(t, http.StatusBadRequest, resp.Code)
}
