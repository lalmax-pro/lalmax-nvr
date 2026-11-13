package transcode

import "testing"

func testProfile(t *testing.T) Profile {
	t.Helper()
	return Profile{ID: "compatible", Name: "Compatible", Revision: 1, Backend: "ffmpeg", Video: VideoSpec{Codec: "h264", Encoder: "libx264", MaxWidth: 1280, MaxHeight: 720, FPS: 15, BitrateKbps: 1500, GOPFrames: 30}, Audio: AudioSpec{Codec: "aac", SampleRate: 48000, Channels: 1, BitrateKbps: 64}}
}

func TestProfileValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Profile)
	}{
		{"unsafe id", func(p *Profile) { p.ID = "../x" }},
		{"backend", func(p *Profile) { p.Backend = "shell" }},
		{"encoder injection", func(p *Profile) { p.Video.Encoder = "libx264 -f file" }},
		{"wrong backend encoder", func(p *Profile) { p.Backend = "gstreamer" }},
		{"odd dimensions", func(p *Profile) { p.Video.MaxWidth = 1279 }},
		{"unbounded fps", func(p *Profile) { p.Video.FPS = 1000 }},
		{"zero bitrate", func(p *Profile) { p.Video.BitrateKbps = 0 }},
		{"copy filters", func(p *Profile) { p.Video.Codec = "copy" }},
		{"invalid audio", func(p *Profile) { p.Audio.Codec = "opus" }},
		{"copy audio parameters", func(p *Profile) { p.Audio.Codec = "copy" }},
	}
	if err := testProfile(t).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testProfile(t)
			tc.change(&p)
			if p.Validate() == nil {
				t.Fatal("accepted invalid profile")
			}
		})
	}
	p := testProfile(t)
	p.Backend = "gstreamer"
	p.Video.Encoder = "x264enc"
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Video = VideoSpec{Codec: "copy"}
	p.Audio = AudioSpec{Codec: "none"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkKey(t *testing.T) {
	p := testProfile(t)
	s := SourceRef{EngineID: "embedded", StreamID: "camera1", AppName: "live"}
	key, err := WorkKey(s, p)
	if err != nil {
		t.Fatal(err)
	}
	p.ID = "renamed"
	p.Name = "Another name"
	p.Revision = 7
	same, err := WorkKey(s, p)
	if err != nil {
		t.Fatal(err)
	}
	if same != key {
		t.Fatal("metadata split equivalent work")
	}
	p.Video.BitrateKbps++
	changed, _ := WorkKey(s, p)
	if changed == key {
		t.Fatal("media changes must change key")
	}
	p = testProfile(t)
	s.AppName = "other"
	changed, _ = WorkKey(s, p)
	if changed == key {
		t.Fatal("different application shared key")
	}
	s.AppName = "live"
	s.EngineID = "remote"
	changed, _ = WorkKey(s, p)
	if changed == key {
		t.Fatal("different engine shared key")
	}
	if _, err = WorkKey(SourceRef{}, p); err == nil {
		t.Fatal("accepted incomplete source")
	}
}

func TestObservedCodecAliasesAreCanonicalized(t *testing.T) {
	p := testProfile(t)
	p.Audio = AudioSpec{Codec: "g711a", SampleRate: 8000, Channels: 1, BitrateKbps: 64}
	for _, alias := range []string{"pcma", "g711a", "G.711A", "alaw"} {
		if err := p.CheckMedia("H264", alias); err != nil {
			t.Fatalf("codec alias %q rejected: %v", alias, err)
		}
	}
}

func TestCopyMediaConstraints(t *testing.T) {
	p := testProfile(t)
	p.Video = VideoSpec{Codec: "copy"}
	p.Audio = AudioSpec{Codec: "copy"}
	for _, audio := range []string{"", "aac"} {
		if err := p.CheckMedia("h264", audio); err != nil {
			t.Fatal(err)
		}
	}
	if p.CheckMedia("h265", "aac") == nil {
		t.Fatal("HEVC copy accepted for H264 output")
	}
	if p.CheckMedia("h264", "opus") == nil {
		t.Fatal("Opus copy accepted for AAC output")
	}
	p = testProfile(t)
	if err := p.CheckMedia("h265", ""); err != nil {
		t.Fatal(err)
	}
	if p.CheckMedia("unknown", "") == nil {
		t.Fatal("unknown video accepted")
	}
}

func TestSourceRejectsURLDelimiters(t *testing.T) {
	for _, value := range []string{"", " ", "camera/path", "camera?token=x", "camera#fragment", "camera\\\\path", "camera\nname"} {
		source := SourceRef{EngineID: "embedded", StreamID: value, AppName: "live"}
		if source.Validate() == nil {
			t.Fatalf("unsafe source accepted: %q", value)
		}
	}
	source := SourceRef{EngineID: "embedded", StreamID: "海康-camera_01", AppName: "live"}
	if err := source.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestJobValidation(t *testing.T) {
	j := Job{ID: "job1", Source: SourceRef{EngineID: "embedded", StreamID: "camera1", AppName: "live"}, ProfileID: "compatible", ProfileRevision: 1, RunPolicy: Always}
	if err := j.Validate(); err != nil {
		t.Fatal(err)
	}
	j.RunPolicy = OnDemand
	if err := j.Validate(); err != nil {
		t.Fatalf("valid on-demand policy rejected: %v", err)
	}
	j.RunPolicy = "unrecognized"
	if j.Validate() == nil {
		t.Fatal("unknown policy accepted")
	}
	j.RunPolicy = Manual
	j.ProfileRevision = 0
	if j.Validate() == nil {
		t.Fatal("unversioned profile accepted")
	}
}
