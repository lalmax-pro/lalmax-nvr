package auth

import "testing"

func TestInternalMediaGrantIsScopedAndRevocable(t *testing.T) {
	a := NewMediaAuthorizer()
	if err := a.ReserveInternalStream("derived", "pub-secret", "play-secret"); err != nil {
		t.Fatal(err)
	}
	if a.PlaybackRequired() {
		t.Fatal("ephemeral grant must not change unrelated playback policy")
	}
	if !a.InternalPlaybackRequired("derived") || a.InternalPlaybackRequired("source") {
		t.Fatal("incorrect stream scope")
	}
	if err := a.AuthorizePublish("rtmp", "derived", "pub-secret"); err != nil {
		t.Fatal(err)
	}
	if err := a.AuthorizePlayback("rtsp", "derived", "play-secret"); err != nil {
		t.Fatal(err)
	}
	if a.AuthorizePlayback("rtsp", "derived", "pub-secret") == nil {
		t.Fatal("publish token authorized playback")
	}
	a.SetPlaybackTokens("media", map[string]string{"other": "configured"})
	if err := a.AuthorizePlayback("rtsp", "derived", "play-secret"); err != nil {
		t.Fatal("reload erased ephemeral grant")
	}
	a.RevokeInternalStream("derived")
	if !a.InternalPlaybackRequired("derived") {
		t.Fatal("revoked stream fell back to open playback")
	}
	if a.AuthorizePlayback("rtsp", "derived", "play-secret") == nil || a.AuthorizePlayback("rtsp", "derived", "") == nil {
		t.Fatal("revoked access accepted")
	}
	if err := a.ReserveInternalStream("derived", "new-pub", "new-play"); err != nil {
		t.Fatal(err)
	}
	if a.AuthorizePlayback("rtsp", "derived", "play-secret") == nil {
		t.Fatal("old credential survived restart")
	}
}
func TestInternalGrantCannotOverrideConfiguredStream(t *testing.T) {
	a := NewMediaAuthorizer()
	a.SetPublishTokens("rtmp", map[string]string{"camera": "existing"})
	if a.ReserveInternalStream("camera", "pub", "play") == nil {
		t.Fatal("configured stream overwritten")
	}
}
