package ban

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/q191201771/lal/pkg/base"
	"github.com/stretchr/testify/require"
)

// s2BanRepo is a scriptable stream ban repository.
type s2BanRepo struct {
	bans        map[string]*storage.StreamBan
	getErr      error
	insertErr   error
	deleteErr   error
	listErr     error
	insertCalls []*storage.StreamBan
}

func (r *s2BanRepo) GetStreamBan(_ context.Context, streamID string) (*storage.StreamBan, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.bans[streamID], nil
}

func (r *s2BanRepo) InsertStreamBan(_ context.Context, ban *storage.StreamBan) error {
	if r.insertErr != nil {
		return r.insertErr
	}
	if r.bans == nil {
		r.bans = make(map[string]*storage.StreamBan)
	}
	r.bans[ban.StreamID] = ban
	r.insertCalls = append(r.insertCalls, ban)
	return nil
}

func (r *s2BanRepo) DeleteStreamBan(_ context.Context, streamID string) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	delete(r.bans, streamID)
	return nil
}

func (r *s2BanRepo) ListStreamBans(context.Context) ([]storage.StreamBan, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := make([]storage.StreamBan, 0, len(r.bans))
	for _, ban := range r.bans {
		out = append(out, *ban)
	}
	return out, nil
}

// s2Pub / s2Sub build lal session info with only the fields the manager reads.
func s2Pub(name, protocol, urlParam string) base.PubStartInfo {
	return base.PubStartInfo{SessionEventCommonInfo: base.SessionEventCommonInfo{
		StreamName: name, Protocol: protocol, UrlParam: urlParam,
	}}
}

func s2Sub(name, protocol, urlParam string) base.SubStartInfo {
	return base.SubStartInfo{SessionEventCommonInfo: base.SessionEventCommonInfo{
		StreamName: name, Protocol: protocol, UrlParam: urlParam,
	}}
}

func TestS2NewManagerDefaults(t *testing.T) {
	m := NewManager(&s2BanRepo{}, nil)
	require.NotNil(t, m.MediaAuthorizer())

	called := false
	m.SetKickFunc(func(context.Context, string) error {
		called = true
		return nil
	})
	require.NoError(t, m.kickFunc(context.Background(), "session"))
	require.True(t, called)

	// Stream keys are installed on the shared authorizer.
	m.SetRTMPStreamKeys(map[string]string{"cam1": "secret"})
	require.NoError(t, m.AuthorizePublish("rtmp", "cam1", "secret"))
	require.Error(t, m.AuthorizePublish("rtmp", "cam1", "wrong"))
	m.SetPublishTokens("srt", map[string]string{"cam2": "key2"})
	require.NoError(t, m.AuthorizePublish("srt", "cam2", "key2"))
}

func TestS2OnPubStartRTMP(t *testing.T) {
	ctx := context.Background()
	_ = ctx

	t.Run("auth disabled still enforces bans", func(t *testing.T) {
		repo := &s2BanRepo{bans: map[string]*storage.StreamBan{"cam1": {StreamID: "cam1", Reason: "abuse"}}}
		m := NewManager(repo, nil)
		m.SetRTMPPublishAuthEnabled(false)
		err := m.OnPubStart(s2Pub("cam1", base.SessionProtocolRtmpStr, ""))
		require.ErrorContains(t, err, "is banned")
		require.ErrorContains(t, err, "abuse")

		// A ban lookup failure fails closed when auth is disabled.
		repo.getErr = errors.New("ban store down")
		err = m.OnPubStart(s2Pub("cam1", base.SessionProtocolRtmpStr, ""))
		require.ErrorContains(t, err, "failed to authorize ")
	})

	t.Run("malformed parameters", func(t *testing.T) {
		repo := &s2BanRepo{bans: map[string]*storage.StreamBan{}}
		m := NewManager(repo, nil)
		err := m.OnPubStart(s2Pub("cam1", base.SessionProtocolRtmpStr, "stream_key=%zz"))
		require.ErrorContains(t, err, "invalid RTMP publish parameters")
	})

	t.Run("token check runs before ban lookup", func(t *testing.T) {
		repo := &s2BanRepo{bans: map[string]*storage.StreamBan{}}
		m := NewManager(repo, auth.NewMediaAuthorizer())
		m.SetRTMPStreamKeys(map[string]string{"cam1": "secret"})
		require.NoError(t, m.OnPubStart(s2Pub("cam1", base.SessionProtocolRtmpStr, "stream_key=secret")))
		require.Error(t, m.OnPubStart(s2Pub("cam1", base.SessionProtocolRtmpStr, "stream_key=other")))
	})
}

func TestS2OnPubStartRTSP(t *testing.T) {
	t.Run("auth disabled enforces bans and fails closed", func(t *testing.T) {
		repo := &s2BanRepo{bans: map[string]*storage.StreamBan{"cam1": {StreamID: "cam1", Reason: ""}}}
		m := NewManager(repo, nil)
		m.SetRTSPPublishAuthEnabled(false)
		err := m.OnPubStart(s2Pub("cam1", base.SessionProtocolRtspStr, ""))
		require.ErrorContains(t, err, `is banned: banned`)

		repo.getErr = errors.New("ban store down")
		err = m.OnPubStart(s2Pub("cam1", base.SessionProtocolRtspStr, ""))
		require.ErrorContains(t, err, "failed to authorize ")
	})

	t.Run("shared publish token required", func(t *testing.T) {
		repo := &s2BanRepo{bans: map[string]*storage.StreamBan{}}
		m := NewManager(repo, auth.NewMediaAuthorizer())
		m.SetPublishTokens("media", map[string]string{"cam1": "token"})

		err := m.OnPubStart(s2Pub("cam1", base.SessionProtocolRtspStr, "token=%zz"))
		require.ErrorContains(t, err, "invalid RTSP publish parameters")

		require.Error(t, m.OnPubStart(s2Pub("cam1", base.SessionProtocolRtspStr, "token=wrong")))
		require.NoError(t, m.OnPubStart(s2Pub("cam1", base.SessionProtocolRtspStr, "token=token")))
	})

	t.Run("anonymous publish still checks bans without failing closed", func(t *testing.T) {
		repo := &s2BanRepo{bans: map[string]*storage.StreamBan{"cam1": {StreamID: "cam1", Reason: "abuse"}}}
		m := NewManager(repo, nil)
		require.ErrorContains(t, m.OnPubStart(s2Pub("cam1", base.SessionProtocolRtspStr, "")), "is banned")

		// An unreadable ban store does not block anonymous publishes.
		repo.getErr = errors.New("ban store down")
		require.NoError(t, m.OnPubStart(s2Pub("cam1", base.SessionProtocolRtspStr, "")))
	})
}

func TestS2OnPubStartOtherProtocols(t *testing.T) {
	repo := &s2BanRepo{bans: map[string]*storage.StreamBan{"cam1": {StreamID: "cam1"}}}
	m := NewManager(repo, nil)
	require.ErrorContains(t, m.OnPubStart(s2Pub("cam1", "srt", "")), "is banned")

	repo.getErr = errors.New("ban store down")
	require.NoError(t, m.OnPubStart(s2Pub("cam1", "srt", "")))
}

func TestS2OnSubStartAndHLS(t *testing.T) {
	repo := &s2BanRepo{bans: map[string]*storage.StreamBan{}}
	m := NewManager(repo, auth.NewMediaAuthorizer())

	// No playback policy: everything is allowed.
	require.NoError(t, m.OnSubStart(s2Sub("cam1", "rtsp", "")))
	require.NoError(t, m.OnHls("cam1", ""))

	// Configure per-stream playback tokens for RTSP and HLS.
	m.MediaAuthorizer().SetPlaybackTokens("rtsp", map[string]string{"cam1": "tok"})
	m.MediaAuthorizer().SetPlaybackTokens("hls", map[string]string{"cam1": "tok"})

	require.ErrorContains(t, m.OnSubStart(s2Sub("cam1", "rtsp", "token=%zz")), "invalid rtsp playback parameters")
	require.Error(t, m.OnSubStart(s2Sub("cam1", "rtsp", "token=bad")))
	require.NoError(t, m.OnSubStart(s2Sub("cam1", "rtsp", "token=tok")))

	require.ErrorContains(t, m.OnHls("cam1", "token=%zz"), "invalid HLS playback parameters")
	require.Error(t, m.OnHls("cam1", "token=bad"))
	require.NoError(t, m.OnHls("cam1", "token=tok"))
}

func TestS2BanStoreOperations(t *testing.T) {
	ctx := context.Background()
	repo := &s2BanRepo{}
	m := NewManager(repo, nil)

	expires := time.Now().Add(time.Hour)
	require.NoError(t, m.Ban(ctx, "cam1", "abuse", &expires))
	require.Len(t, repo.insertCalls, 1)
	require.Equal(t, "abuse", repo.insertCalls[0].Reason)
	require.NotNil(t, repo.insertCalls[0].ExpiresAt)

	bans, err := m.ListBans(ctx)
	require.NoError(t, err)
	require.Len(t, bans, 1)

	require.NoError(t, m.Unban(ctx, "cam1"))
	require.Empty(t, repo.bans)

	// Error propagation.
	repo.insertErr = errors.New("insert exploded")
	require.ErrorContains(t, m.Ban(ctx, "cam1", "abuse", nil), "insert exploded")
	repo.deleteErr = errors.New("delete exploded")
	require.ErrorContains(t, m.Unban(ctx, "cam1"), "delete exploded")
	repo.listErr = errors.New("list exploded")
	_, err = m.ListBans(ctx)
	require.ErrorContains(t, err, "list exploded")
}
