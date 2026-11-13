package transcode

import (
	"context"
	"fmt"
	"strconv"
)

// gstProperty quotes values for GStreamer's pipeline parser (not a shell).
// Passing argv directly does not by itself escape the embedded pipeline grammar.
func gstProperty(name, value string) string { return name + "=" + strconv.Quote(value) }

// GStreamerArgs builds a fixed RTSP->FLV/RTMP pipeline from observed codecs.
// Explicit video dimensions are required to preserve aspect ratio without
// upscaling; probing the input belongs to the trusted media adapter.
func GStreamerArgs(spec JobSpec, width, height int) ([]string, error) {
	if err := spec.validate("gstreamer"); err != nil {
		return nil, err
	}
	video := normalizeObservedCodec(spec.VideoCodec)
	codec := "H264"
	depay := "rtph264depay"
	parser := "h264parse"
	decoder := "avdec_h264"
	if video == "h265" || video == "hevc" {
		codec = "H265"
		depay = "rtph265depay"
		parser = "h265parse"
		decoder = "avdec_h265"
	}
	args := []string{"-e", "rtspsrc", gstProperty("location", spec.InputURL), "protocols=tcp", "latency=200", "name=src", "flvmux", "name=mux", "streamable=true", "!", "rtmpsink", gstProperty("location", spec.OutputURL), "src.", "!", "application/x-rtp,media=video,encoding-name=" + codec, "!", "queue", "!", depay, "!", parser}
	if spec.Profile.Video.Codec != "copy" {
		w, h, err := outputDimensions(width, height, spec.Profile.Video)
		if err != nil {
			return nil, err
		}
		v := spec.Profile.Video
		args = append(args, "!", decoder, "!", "videoconvert", "!", "videoscale", "!", "videorate", "!", fmt.Sprintf("video/x-raw,format=I420,width=%d,height=%d,framerate=%d/1,pixel-aspect-ratio=1/1", w, h, v.FPS), "!", "x264enc", "tune=zerolatency", "speed-preset=veryfast", "bframes=0", "threads="+strconv.Itoa(spec.Threads), "bitrate="+strconv.Itoa(v.BitrateKbps), "key-int-max="+strconv.Itoa(v.GOPFrames), "!", "h264parse")
	}
	args = append(args, "!", "video/x-h264,stream-format=avc,alignment=au", "!", "queue", "!", "mux.video")
	audio := normalizeObservedCodec(spec.AudioCodec)
	a := spec.Profile.Audio
	if audio == "" || a.Codec == "none" {
		return args, nil
	}
	var audioElements []string
	switch audio {
	case "aac":
		audioElements = []string{"application/x-rtp,media=audio,encoding-name=MPEG4-GENERIC", "!", "rtpmp4gdepay", "!", "aacparse"}
	case "g711a":
		audioElements = []string{"application/x-rtp,media=audio,encoding-name=PCMA", "!", "rtppcmadepay", "!", "alawdec"}
	case "g711u":
		audioElements = []string{"application/x-rtp,media=audio,encoding-name=PCMU", "!", "rtppcmudepay", "!", "mulawdec"}
	default:
		return nil, &Error{Code: "unsupported_input", Message: "unsupported GStreamer audio input"}
	}
	args = append(args, "src.", "!")
	// Queue immediately after the dynamic pad caps: a downstream decoder or
	// mux must not block rtspsrc from exposing the other media pad.
	args = append(args, audioElements[0], "!", "queue", "!")
	args = append(args, audioElements[2:]...)
	if a.Codec != "copy" {
		if audio == "aac" {
			args = append(args, "!", "avdec_aac")
		}
		args = append(args, "!", "audioconvert", "!", "audioresample", "!", fmt.Sprintf("audio/x-raw,rate=%d,channels=%d", a.SampleRate, a.Channels))
		if a.Codec == "g711a" {
			args = append(args, "!", "alawenc")
		} else {
			args = append(args, "!", "avenc_aac", "bitrate="+strconv.Itoa(a.BitrateKbps*1000), "!", "aacparse")
		}
	}
	args = append(args, "!", "queue", "!", "mux.audio")
	return args, nil
}

func outputDimensions(width, height int, v VideoSpec) (int, int, error) {
	if width < 2 || height < 2 || width > 16384 || height > 16384 {
		return 0, 0, invalid("source dimensions are required for GStreamer resizing")
	}
	w, h := width, height
	if w > v.MaxWidth {
		h = h * v.MaxWidth / w
		w = v.MaxWidth
	}
	if h > v.MaxHeight {
		w = w * v.MaxHeight / h
		h = v.MaxHeight
	}
	w -= w % 2
	h -= h % 2
	if w < 2 || h < 2 {
		return 0, 0, invalid("source aspect ratio cannot fit target dimensions")
	}
	return w, h, nil
}

func StartGStreamer(ctx context.Context, path string, spec JobSpec, width, height int) (Process, error) {
	args, err := GStreamerArgs(spec, width, height)
	if err != nil {
		return nil, err
	}
	return startCommand(ctx, path, args)
}
