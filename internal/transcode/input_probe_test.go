package transcode

import (
	"context"
	"testing"
)

func TestInputDimensionParsing(t *testing.T) {
	w, h, err := parseInputDimensions([]byte(`{"streams":[{"width":1920,"height":1080}]}`))
	if err != nil || w != 1920 || h != 1080 {
		t.Fatal("valid dimensions rejected")
	}
	for _, bad := range []string{`{}`, `{"streams":[]}`, `{"streams":[{"width":0,"height":1080}]}`, `{"streams":[{"width":99999,"height":1080}]}`, `{"streams":[{},{}]}`} {
		if _, _, err := parseInputDimensions([]byte(bad)); err == nil {
			t.Fatal("invalid dimensions accepted")
		}
	}
}
func TestInputProbeRejectsLocalFiles(t *testing.T) {
	if _, _, err := ProbeInputDimensions(context.Background(), "ffprobe", "file:///etc/passwd", fakeProbeRunner{}); err == nil {
		t.Fatal("local file accepted")
	}
}
