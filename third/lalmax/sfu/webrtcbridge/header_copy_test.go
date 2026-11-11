package webrtcbridge

import (
	"sync"
	"testing"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

func TestCopyRTPHeaderIsolatesConcurrentWrites(t *testing.T) {
	factory := CopyRTPHeaderFactory{}
	inst, err := factory.NewInterceptor("test")
	require.NoError(t, err)
	header := &rtp.Header{Version: 2, SSRC: 7, SequenceNumber: 42}
	require.NoError(t, header.SetExtension(9, []byte{7}))
	writer := inst.BindLocalStream(&interceptor.StreamInfo{}, interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
		if err := h.SetExtension(3, []byte{1, 2}); err != nil {
			return 0, err
		}
		if err := h.SetExtension(9, []byte{8}); err != nil {
			return 0, err
		}
		return len(payload), nil
	}))
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if _, err := writer.Write(header, []byte{1}, nil); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	require.Equal(t, []byte{7}, header.GetExtension(9))
	require.Nil(t, header.GetExtension(3))
}
