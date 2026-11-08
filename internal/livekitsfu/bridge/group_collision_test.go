package bridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/testutil/rtpforward"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
)

type groupHook struct{ messages chan base.RtmpMsg }

func (h *groupHook) OnMsg(m base.RtmpMsg) { h.messages <- m.Clone() }
func (h *groupHook) OnStop()              {}

// A duplicate target must leave an existing publisher and its viewers intact,
// including when both tracks of an AV bridge must be released on failure.
func TestLalGroupCollisionPreservesExistingPublisher(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, kind := range []string{"video", "audio", "audio-video"} {
		t.Run(kind, func(t *testing.T) {
			server := logic.NewServerManager(func(o *logic.Option) {
				o.ConfRawContent = []byte(`{"hls":{"enable":true,"on_demand":true,"url_pattern":"/hls/"}}`)
			})
			hook := &groupHook{messages: make(chan base.RtmpMsg, 8)}
			server.WithOnHookSession(func(_, _ string) logic.ICustomizeHookSessionContext { return hook })
			existing, err := server.AddCustomizePubSession("occupied")
			if err != nil {
				t.Fatal(err)
			}
			defer server.DelCustomizePubSession(existing)
			forward, err := rtpforward.New(2, 4)
			if err != nil {
				t.Fatal(err)
			}
			defer forward.Close()
			first, err := forward.Subscribe("first")
			if err != nil {
				t.Fatal(err)
			}
			var second *rtpforward.Subscriber
			switch kind {
			case "video":
				err = RunH264ToLal(context.Background(), first, server, "occupied")
			case "audio":
				err = RunOpusToLal(context.Background(), first, server, "occupied")
			default:
				second, err = forward.Subscribe("second")
				if err != nil {
					t.Fatal(err)
				}
				err = RunAVToLal(context.Background(), first, second, server, "occupied", func(context.Context) (AVReferences, error) { return AVReferences{}, nil })
			}
			if !errors.Is(err, base.ErrDupInStream) {
				t.Fatalf("collision: %v", err)
			}
			for _, sub := range []*rtpforward.Subscriber{first, second} {
				if sub != nil && !errors.Is(sub.Err(), rtpforward.ErrSubscriberClosed) {
					t.Fatal("failed bridge retained its subscription")
				}
			}
			if group := server.GetGroup("live", "occupied"); group == nil || !group.HasInSession() {
				t.Fatal("collision displaced existing publisher")
			}
			// Send a real message through the original session after the failed
			// bridge, and verify the Group still delivers it to its hook.
			payload := []byte{0xaf, 0x01, 0x21, 0x10}
			msg := base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio, MsgLen: uint32(len(payload))}, Payload: payload}
			if err := existing.FeedRtmpMsg(msg); err != nil {
				t.Fatalf("original publisher stopped: %v", err)
			}
			select {
			case got := <-hook.messages:
				if got.Header.MsgTypeId != msg.Header.MsgTypeId || string(got.Payload) != string(payload) {
					t.Fatalf("unexpected message: %+v", got)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("existing publisher no longer reaches the group")
			}
		})
	}
}
