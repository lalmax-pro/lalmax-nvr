package media

import (
	"testing"

	"github.com/bluenviron/gohlslib"
	"github.com/bluenviron/gohlslib/pkg/codecs"
	"github.com/q191201771/lal/pkg/base"
	"github.com/stretchr/testify/require"
)

func TestAnnexBFromAU(t *testing.T) {
	got := AnnexBFromAU([][]byte{{0x67, 0x42}, {}, {0x68, 0xce}})
	require.Equal(t, []byte{0, 0, 0, 1, 0x67, 0x42, 0, 0, 0, 1, 0x68, 0xce}, got)
	require.Nil(t, AnnexBFromAU(nil))
}

func TestClassifyHLSTracks_H264AAC(t *testing.T) {
	info, err := ClassifyHLSTracks([]*gohlslib.Track{
		{Codec: &codecs.H264{SPS: []byte{0x67}, PPS: []byte{0x68}}},
		{Codec: &codecs.MPEG4Audio{}},
	})
	require.NoError(t, err)
	require.Equal(t, "h264", info.VideoCodec)
	require.Equal(t, "aac", info.AudioCodec)
	require.Equal(t, base.AvPacketPtAvc, info.VideoPT)
	require.True(t, info.Playable)
	require.True(t, info.Recordable)
	require.NotEmpty(t, info.VideoParamSets)
}

func TestClassifyHLSTracks_RejectsAV1(t *testing.T) {
	_, err := ClassifyHLSTracks([]*gohlslib.Track{{Codec: &codecs.AV1{}}})
	require.ErrorIs(t, err, ErrHLSPullUnsupportedCodec)
}

func TestEngineSupportsCustomizePub(t *testing.T) {
	require.False(t, engineSupportsCustomizePub(nil))
	require.False(t, engineSupportsCustomizePub(&LalmaxHTTP{}))
}
