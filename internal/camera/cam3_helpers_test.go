package camera

// cam3* test helpers: a loopback ONVIF device that answers the SOAP calls the
// camera manager performs. No real hardware and no external network is used.

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

const (
	// cam3BaseToken is replaced with the fake server base URL inside configured
	// snapshot URIs, so responses can point back at the same loopback server.
	cam3BaseToken  = "{cam3base}"
	cam3SOAPPrefix = `<?xml version="1.0" encoding="UTF-8"?>` +
		`<env:Envelope xmlns:env="http://www.w3.org/2003/05/soap-envelope"><env:Body>`
	cam3SOAPSuffix = `</env:Body></env:Envelope>`
)

type cam3Profile struct {
	Token       string
	Name        string
	Encoding    string
	VideoSource string
}

type cam3Encoder struct {
	Token    string
	Encoding string
}

// cam3FakeONVIF is a minimal ONVIF device server. Only the operations the
// camera manager uses are implemented.
type cam3FakeONVIF struct {
	Profiles     []cam3Profile
	Encoders     []cam3Encoder
	Snapshots    map[string]string // profile token -> snapshot URL (cam3BaseToken allowed)
	StreamURIs   map[string]string // profile token -> stream URI; empty value = empty URI
	NoProfiles   bool
	FailProfiles bool
	FailStreamUI bool
	FailSnapshot bool
	FailEncoders bool
	FailEvents   bool
	Serial       string
	Manufacturer string
	Model        string
	SnapshotJPEG []byte

	srv   *httptest.Server
	mu    sync.Mutex
	calls map[string]int
}

func cam3NewFakeONVIF(t *testing.T, dev *cam3FakeONVIF) *cam3FakeONVIF {
	t.Helper()
	if dev.Serial == "" {
		dev.Serial = "CAM3-SERIAL"
	}
	if dev.Manufacturer == "" {
		dev.Manufacturer = "Cam3Vendor"
	}
	if dev.Model == "" {
		dev.Model = "Cam3Model"
	}
	if len(dev.SnapshotJPEG) == 0 {
		dev.SnapshotJPEG = cam3JPEGBytes(t)
	}
	dev.calls = make(map[string]int)
	dev.srv = httptest.NewServer(http.HandlerFunc(dev.handle))
	t.Cleanup(dev.srv.Close)
	return dev
}

// URL is the device service endpoint handed to the camera manager.
func (d *cam3FakeONVIF) URL() string { return d.srv.URL + "/onvif/device_service" }

// BaseURL is the loopback server root.
func (d *cam3FakeONVIF) BaseURL() string { return d.srv.URL }

func (d *cam3FakeONVIF) callCount(op string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[op]
}

func (d *cam3FakeONVIF) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	text := string(body)
	if r.URL.Path == "/snapshot.jpg" {
		if len(d.SnapshotJPEG) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(d.SnapshotJPEG)
		return
	}

	op := cam3Operation(text)
	d.mu.Lock()
	d.calls[op]++
	d.mu.Unlock()

	base := "http://" + r.Host
	var inner string
	switch op {
	case "GetSystemDateAndTime":
		inner = `<GetSystemDateAndTimeResponse><SystemDateAndTime><UTCDateTime>` +
			`<Time><Hour>12</Hour><Minute>0</Minute><Second>0</Second></Time>` +
			`<Date><Year>2024</Year><Month>6</Month><Day>15</Day></Date>` +
			`</UTCDateTime></SystemDateAndTime></GetSystemDateAndTimeResponse>`
	case "GetServices":
		inner = cam3ServicesXML(base)
	case "GetDeviceInformation":
		inner = `<GetDeviceInformationResponse><Manufacturer>` + d.Manufacturer +
			`</Manufacturer><Model>` + d.Model +
			`</Model><FirmwareVersion>1.0.0</FirmwareVersion><SerialNumber>` + d.Serial +
			`</SerialNumber><HardwareId>HW-CAM3</HardwareId></GetDeviceInformationResponse>`
	case "GetProfiles":
		if d.FailProfiles {
			http.Error(w, "profiles unavailable", http.StatusInternalServerError)
			return
		}
		inner = cam3ProfilesXML(d)
	case "GetStreamUri":
		if d.FailStreamUI {
			http.Error(w, "stream uri unavailable", http.StatusInternalServerError)
			return
		}
		token := cam3TagValue(text, "ProfileToken")
		uri := "rtsp://127.0.0.1:1/stream/" + token
		if configured, ok := d.StreamURIs[token]; ok {
			uri = configured
		}
		inner = `<GetStreamUriResponse><MediaUri><Uri>` + uri +
			`</Uri><InvalidAfterConnect>false</InvalidAfterConnect><Timeout>PT0S</Timeout></MediaUri></GetStreamUriResponse>`
	case "GetSnapshotUri":
		if d.FailSnapshot {
			http.Error(w, "snapshot uri unavailable", http.StatusInternalServerError)
			return
		}
		token := cam3TagValue(text, "ProfileToken")
		uri := d.Snapshots[token]
		uri = strings.ReplaceAll(uri, cam3BaseToken, base)
		inner = `<GetSnapshotUriResponse><MediaUri><Uri>` + uri + `</Uri></MediaUri></GetSnapshotUriResponse>`
	case "GetVideoEncoderConfigurations":
		if d.FailEncoders {
			http.Error(w, "encoder configurations unavailable", http.StatusInternalServerError)
			return
		}
		var b strings.Builder
		b.WriteString(`<GetVideoEncoderConfigurationsResponse>`)
		for _, cfg := range d.Encoders {
			b.WriteString(`<Configurations token="` + cfg.Token + `"><Name>` + cfg.Token +
				`</Name><Encoding>` + cfg.Encoding + `</Encoding></Configurations>`)
		}
		b.WriteString(`</GetVideoEncoderConfigurationsResponse>`)
		inner = b.String()
	case "CreatePullPointSubscription":
		if d.FailEvents {
			http.Error(w, "events unavailable", http.StatusInternalServerError)
			return
		}
		inner = `<CreatePullPointSubscriptionResponse><SubscriptionReference><Address>` +
			base + `/onvif/subscription</Address></SubscriptionReference>` +
			`<CurrentTime>2024-06-15T12:00:00Z</CurrentTime>` +
			`<TerminationTime>2024-06-16T12:00:00Z</TerminationTime></CreatePullPointSubscriptionResponse>`
	case "PullMessages":
		inner = `<PullMessagesResponse><CurrentTime>2024-06-15T12:00:00Z</CurrentTime>` +
			`<TerminationTime>2024-06-16T12:00:00Z</TerminationTime></PullMessagesResponse>`
	case "Unsubscribe":
		inner = `<UnsubscribeResponse/>`
	case "Renew":
		inner = `<RenewResponse><TerminationTime>2024-06-16T12:00:00Z</TerminationTime></RenewResponse>`
	default:
		http.Error(w, "cam3 fake onvif: unhandled operation "+op, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, cam3SOAPPrefix+inner+cam3SOAPSuffix)
}

// cam3Operation identifies the ONVIF operation from a SOAP request body.
// Longer names come first so a shared prefix cannot shadow them.
func cam3Operation(body string) string {
	for _, op := range []string{
		"GetSystemDateAndTime",
		"GetDeviceInformation",
		"GetVideoEncoderConfigurations",
		"GetSnapshotUri",
		"GetStreamUri",
		"GetProfiles",
		"GetServices",
		"CreatePullPointSubscription",
		"PullMessages",
		"Unsubscribe",
		"Renew",
	} {
		if strings.Contains(body, "<"+op+" ") || strings.Contains(body, "<"+op+">") {
			return op
		}
	}
	return ""
}

func cam3ServicesXML(base string) string {
	type service struct{ ns, path string }
	services := []service{
		{"http://www.onvif.org/ver10/device/wsdl", "/onvif/device"},
		{"http://www.onvif.org/ver10/media/wsdl", "/onvif/media"},
		{"http://www.onvif.org/ver20/ptz/wsdl", "/onvif/ptz"},
		{"http://www.onvif.org/ver20/imaging/wsdl", "/onvif/imaging"},
		{"http://www.onvif.org/ver10/events/wsdl", "/onvif/events"},
	}
	var b strings.Builder
	b.WriteString(`<GetServicesResponse>`)
	for _, svc := range services {
		b.WriteString(`<Service><Namespace>` + svc.ns + `</Namespace><XAddr>` + base + svc.path +
			`</XAddr><Version><Major>2</Major><Minor>0</Minor></Version></Service>`)
	}
	b.WriteString(`</GetServicesResponse>`)
	return b.String()
}

func cam3ProfilesXML(dev *cam3FakeONVIF) string {
	if dev.NoProfiles {
		return `<GetProfilesResponse/>`
	}
	var b strings.Builder
	b.WriteString(`<GetProfilesResponse>`)
	for _, p := range dev.Profiles {
		b.WriteString(`<Profiles token="` + p.Token + `"><Name>` + p.Name + `</Name>`)
		if p.VideoSource != "" {
			b.WriteString(`<VideoSourceConfiguration token="` + p.VideoSource + `"/>`)
		}
		b.WriteString(`<VideoEncoderConfiguration token="enc-` + p.Token + `"><Encoding>` + p.Encoding +
			`</Encoding><Resolution><Width>1920</Width><Height>1080</Height></Resolution></VideoEncoderConfiguration>`)
		b.WriteString(`</Profiles>`)
	}
	b.WriteString(`</GetProfilesResponse>`)
	return b.String()
}

func cam3TagValue(body, tag string) string {
	open, closing := "<"+tag+">", "</"+tag+">"
	start := strings.Index(body, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(body[start:], closing)
	if end < 0 {
		return ""
	}
	return body[start : start+end]
}

func cam3JPEGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 30, G: 120, B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}))
	return buf.Bytes()
}

// cam3NewManager builds a manager with a temp storage root and no database.
func cam3NewManager(t *testing.T, cams ...config.CameraConfig) (*CameraManager, *storage.Manager) {
	t.Helper()
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	cfg := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "1m"},
		Cameras: cams,
	}
	mgr := NewCameraManager(cfg, store, nil, "")
	t.Cleanup(func() { _ = mgr.Stop() })
	return mgr, store
}

func cam3ONVIFCamera(id, endpoint, profileToken string) config.CameraConfig {
	return config.CameraConfig{
		ID:            id,
		Name:          id,
		Protocol:      "onvif",
		Encoding:      "h264",
		ONVIFEndpoint: endpoint,
		URL:           endpoint,
		ProfileToken:  profileToken,
		Username:      "admin",
		Password:      "secret",
		Enabled:       true,
	}
}

// cam3DefaultDevice is the usual two-profile device: "sub" has no snapshot URI,
// so callers exercise the fallback to "main".
func cam3DefaultDevice() *cam3FakeONVIF {
	return &cam3FakeONVIF{
		Profiles: []cam3Profile{
			{Token: "sub", Name: "SubStream", Encoding: "h264", VideoSource: "vsrc-sub"},
			{Token: "main", Name: "MainStream", Encoding: "h264", VideoSource: "vsrc-main"},
		},
		Snapshots: map[string]string{"main": cam3BaseToken + "/snapshot.jpg"},
		Serial:    "CAM3-SERIAL",
	}
}

// cam3WaitFor polls until cond is true or the deadline expires.
func cam3WaitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, timeout, 20*time.Millisecond)
}
