package onvif

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestONVIF2606DigestSHA256AuthInt(t *testing.T) {
	const path = "/onvif/a%2Fb?channel=1&mode=x"
	const realm = "camera, channel 1"
	param := func(header, name string) string {
		match := regexp.MustCompile(`(?:^Digest |, )` + name + `=(?:"([^"]*)"|([^, ]+))`).FindStringSubmatch(header)
		if match == nil {
			return ""
		}
		return match[1] + match[2]
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		header := r.Header.Get("Authorization")
		assert.NotContains(t, string(body), "PasswordText")
		if header == "" {
			w.Header().Add("WWW-Authenticate", `Basic realm="ignored", Digest realm="legacy", nonce="md5", qop="auth", algorithm=MD5`)
			w.Header().Add("WWW-Authenticate", `Digest realm="camera, channel 1", nonce="sha", qop="auth-int", algorithm=SHA-256`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		assert.Equal(t, path, r.RequestURI)
		assert.Equal(t, path, param(header, "uri"))
		assert.Equal(t, realm, param(header, "realm"))
		assert.Equal(t, "SHA-256", param(header, "algorithm"))
		assert.Equal(t, "auth-int", param(header, "qop"))
		nc := fmt.Sprintf("%08x", requests.Add(1))
		assert.Equal(t, nc, param(header, "nc"))
		ha1 := fmt.Sprintf("%x", sha256.Sum256([]byte("user:"+realm+":camera-password")))
		ha2 := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("POST:%s:%x", path, sha256.Sum256(body)))))
		expected := sha256.Sum256([]byte(ha1 + ":sha:" + nc + ":" + param(header, "cnonce") + ":auth-int:" + ha2))
		if assert.Equal(t, fmt.Sprintf("%x", expected), param(header, "response")) {
			_, _ = io.WriteString(w, soapOK("<GetDeviceInformationResponse/>"))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client := NewSOAPClient(server.Client(), "user", "camera-password")
	for range 2 {
		_, err := client.SendContext(t.Context(), &SOAPRequest{ServiceURL: server.URL + path, Body: "<GetDeviceInformation/>"})
		require.NoError(t, err)
	}
	require.EqualValues(t, 2, requests.Load())
}

func TestONVIF2606PasswordTextRequiresVerifiedTLS(t *testing.T) {
	for _, insecureTLS := range []bool{false, true} {
		t.Run(strconv.FormatBool(insecureTLS), func(t *testing.T) {
			var receivedText atomic.Bool
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), "PasswordText") || strings.Contains(string(body), "camera-password") {
					receivedText.Store(true)
				}
				w.WriteHeader(http.StatusUnauthorized)
			})
			var server *httptest.Server
			if insecureTLS {
				server = httptest.NewTLSServer(handler)
			} else {
				server = httptest.NewServer(handler)
			}
			defer server.Close()
			hc := server.Client()
			if insecureTLS {
				hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
			}
			client := NewSOAPClient(hc, "user", "camera-password")
			_, err := client.SendContext(t.Context(), &SOAPRequest{ServiceURL: server.URL, Body: "<Get/>"})
			require.ErrorContains(t, err, "authentication failed (401)")
			require.False(t, receivedText.Load())
		})
	}
}

func TestONVIF2606ServiceURLAndRedirectStayOnDevice(t *testing.T) {
	var otherRequests atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		otherRequests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer other.Close()
	otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
			return
		}
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(string(body), "GetSystemDateAndTime"):
			_, _ = io.WriteString(w, deviceDateTimeXML())
		case strings.Contains(string(body), "GetServices"):
			_, _ = fmt.Fprintf(w, "%s", soapOK(fmt.Sprintf(`<GetServicesResponse><Service>
				<Namespace>%s</Namespace><XAddr>%s/onvif/media</XAddr>
			</Service></GetServicesResponse>`, media2Namespace, otherURL)))
		}
	}))
	defer origin.Close()
	client := newTestClient(t, origin)
	require.NoError(t, client.Connect(t.Context()))
	_, err := client.MediaService().GetProfiles(t.Context())
	require.ErrorContains(t, err, "changed device host or downgraded HTTPS")
	_, err = client.soap.SendContext(t.Context(), &SOAPRequest{ServiceURL: origin.URL + "/redirect", Body: "<Get/>"})
	require.ErrorContains(t, err, "HTTP error 307")
	require.Zero(t, otherRequests.Load())
}

func TestONVIF2606SOAPFaultAndCancellation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, soapOK(`<s:Fault xmlns:ter="http://www.onvif.org/ver10/error">
			<s:Code><s:Value>s:Receiver</s:Value><s:Subcode><s:Value>ter:ActionNotSupported</s:Value>
			<s:Subcode><s:Value>ter:NotImplemented</s:Value></s:Subcode></s:Subcode></s:Code>
			<s:Reason><s:Text xml:lang="en">Unsupported action</s:Text></s:Reason><s:Detail><ter:Reason>unsupported</ter:Reason></s:Detail>
		</s:Fault>`))
	}))
	defer server.Close()
	client := NewSOAPClient(server.Client(), "", "")
	req := &SOAPRequest{ServiceURL: server.URL, Body: "<Get/>"}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := client.SendContext(ctx, req)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, requests.Load())
	_, err = client.SendContext(t.Context(), req)
	var fault *SOAPFault
	require.ErrorAs(t, err, &fault)
	require.Equal(t, []string{"ter:ActionNotSupported", "ter:NotImplemented"}, fault.Subcodes)
	require.Contains(t, fault.Detail, "unsupported")
}

func TestONVIF2606Media2ProfilesAndTransport(t *testing.T) {
	const media2 = "http://www.onvif.org/ver20/media/wsdl"
	server := newONVIFDeviceServer(t, func(body string) string {
		if strings.Contains(body, "GetProfiles") {
			return soapOK(`<tr2:GetProfilesResponse xmlns:tr2="http://www.onvif.org/ver20/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
				<tr2:Profiles token="main&amp;1"><tr2:Name>Main</tr2:Name><tr2:Configurations>
					<tr2:VideoSource token="source-config"><tt:Name>Video source</tt:Name><tt:UseCount>1</tt:UseCount>
						<tt:SourceToken>physical-sensor</tt:SourceToken><tt:Bounds x="0" y="0" width="1920" height="1080"/>
					</tr2:VideoSource>
					<tr2:VideoEncoder token="encoder" GovLength="60" Profile="Main"><tt:Name>Main encoder</tt:Name><tt:UseCount>1</tt:UseCount>
						<tt:Encoding>H265</tt:Encoding><tt:Resolution><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:Resolution>
						<tt:RateControl><tt:FrameRateLimit>29.97</tt:FrameRateLimit><tt:BitrateLimit>4096</tt:BitrateLimit></tt:RateControl>
						<tt:Quality>3.5</tt:Quality>
					</tr2:VideoEncoder>
				</tr2:Configurations></tr2:Profiles></tr2:GetProfilesResponse>`)
		}
		var req struct {
			XMLName      xml.Name `xml:"http://www.onvif.org/ver20/media/wsdl GetStreamUri"`
			Protocol     string   `xml:"http://www.onvif.org/ver20/media/wsdl Protocol"`
			ProfileToken string   `xml:"http://www.onvif.org/ver20/media/wsdl ProfileToken"`
		}
		assert.NoError(t, ParseResponseBody([]byte(body), &req))
		assert.Equal(t, "main&1", req.ProfileToken)
		if req.Protocol == "RTSP" {
			return soapOK(`<s:Fault><s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>ter:InvalidStreamSetup</s:Value></s:Subcode></s:Code></s:Fault>`)
		}
		assert.Equal(t, "RtspUnicast", req.Protocol)
		return soapOK(`<GetStreamUriResponse xmlns="` + media2 + `"><Uri>rtsp://127.0.0.1/main</Uri></GetStreamUriResponse>`)
	})

	media := NewMediaService(serviceClient(t, server, false, true, false, false))
	profiles, err := media.GetProfiles(t.Context())
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	require.Equal(t, "H265", profiles[0].Encoding)
	require.Equal(t, 29.97, profiles[0].Framerate)
	require.Equal(t, "physical-sensor", profiles[0].VideoSourceToken)
	uri, err := media.GetStreamURI(t.Context(), profiles[0].Token)
	require.NoError(t, err)
	require.Equal(t, "rtsp://127.0.0.1/main", uri)
}

func TestONVIF2606Media2EncoderOptionsAndWrite(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		if strings.Contains(body, "GetVideoEncoderConfigurationOptions") {
			assert.Contains(t, body, `xmlns="http://www.onvif.org/ver20/media/wsdl"`)
			return soapOK(`<tr2:GetVideoEncoderConfigurationOptionsResponse xmlns:tr2="http://www.onvif.org/ver20/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
				<tr2:Options FrameRatesSupported="29.97 15" ProfilesSupported="Main"><tt:Encoding>H265</tt:Encoding>
					<tt:QualityRange><tt:Min>1</tt:Min><tt:Max>5</tt:Max></tt:QualityRange>
					<tt:ResolutionsAvailable><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:ResolutionsAvailable>
					<tt:BitrateRange><tt:Min>1024</tt:Min><tt:Max>8192</tt:Max></tt:BitrateRange>
				</tr2:Options></tr2:GetVideoEncoderConfigurationOptionsResponse>`)
		}
		var req struct {
			XMLName       xml.Name `xml:"http://www.onvif.org/ver20/media/wsdl SetVideoEncoderConfiguration"`
			Configuration struct {
				Token       string `xml:"token,attr"`
				Name        string `xml:"http://www.onvif.org/ver10/schema Name"`
				RateControl struct {
					FrameRate float64 `xml:"http://www.onvif.org/ver10/schema FrameRateLimit"`
				} `xml:"http://www.onvif.org/ver10/schema RateControl"`
			} `xml:"http://www.onvif.org/ver20/media/wsdl Configuration"`
		}
		assert.NoError(t, ParseResponseBody([]byte(body), &req))
		assert.Equal(t, "cfg&1", req.Configuration.Token)
		assert.Equal(t, "Test <camera>", req.Configuration.Name)
		assert.Equal(t, 29.97, req.Configuration.RateControl.FrameRate)
		assert.NotContains(t, body, "ForcePersistence")
		return soapOK(`<SetVideoEncoderConfigurationResponse xmlns="http://www.onvif.org/ver20/media/wsdl"/>`)
	})
	media := NewMediaService(serviceClient(t, server, false, true, false, false))
	options, err := media.GetVideoEncoderConfigurationOptions(t.Context(), "profile")
	require.NoError(t, err)
	require.Equal(t, "H265", options["encoding"])
	require.Equal(t, Range{Min: 15, Max: 29.97}, options["framerate_range"])
	err = media.SetVideoEncoderConfiguration(t.Context(), VideoEncoderConfiguration{
		Token: "cfg&1", Name: "Test <camera>", Encoding: "H265", Profile: "Main", GovLength: 60,
		Resolution: Resolution{Width: 1920, Height: 1080}, FramerateLimit: 29.97, BitrateLimit: 4096, Quality: 3.5,
	})
	require.NoError(t, err)
}

func TestONVIF2606RecordingSearchStandardPage(t *testing.T) {
	var pages atomic.Int32
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "FindRecordings"):
			var req struct {
				XMLName xml.Name `xml:"http://www.onvif.org/ver10/search/wsdl FindRecordings"`
				Scope   struct {
					Recording string `xml:"http://www.onvif.org/ver10/schema IncludedRecordings"`
				} `xml:"http://www.onvif.org/ver10/search/wsdl Scope"`
				KeepAlive string `xml:"http://www.onvif.org/ver10/search/wsdl KeepAliveTime"`
			}
			assert.NoError(t, ParseResponseBody([]byte(body), &req))
			assert.Equal(t, "cam&1", req.Scope.Recording)
			assert.Equal(t, "PT10S", req.KeepAlive)
			assert.NotContains(t, body, "IncludedTimes")
			return soapOK(`<FindRecordingsResponse xmlns="http://www.onvif.org/ver10/search/wsdl"><SearchToken>search&amp;1</SearchToken></FindRecordingsResponse>`)
		case strings.Contains(body, "GetRecordingSearchResults"):
			assert.Contains(t, body, "<SearchToken>search&amp;1</SearchToken><MinResults>1</MinResults><MaxResults>5</MaxResults><WaitTime>PT5S</WaitTime>")
			if pages.Add(1) == 1 {
				return soapOK(`<GetRecordingSearchResultsResponse xmlns="http://www.onvif.org/ver10/search/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
					<ResultList><tt:SearchState>Searching</tt:SearchState><tt:RecordingInformation>
						<tt:RecordingToken>cam&amp;1</tt:RecordingToken><tt:Source><tt:SourceId>urn:camera:1</tt:SourceId>
						<tt:Name>Camera A</tt:Name><tt:Location>Entrance</tt:Location><tt:Description>Video</tt:Description><tt:Address>http://camera</tt:Address></tt:Source>
						<tt:EarliestRecording>2026-11-17T00:00:00Z</tt:EarliestRecording><tt:LatestRecording>2026-11-17T01:00:00Z</tt:LatestRecording>
						<tt:Content>Recording</tt:Content><tt:RecordingStatus>Recording</tt:RecordingStatus>
					</tt:RecordingInformation></ResultList></GetRecordingSearchResultsResponse>`)
			}
			return soapOK(`<GetRecordingSearchResultsResponse xmlns="http://www.onvif.org/ver10/search/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
				<ResultList><tt:SearchState>Completed</tt:SearchState></ResultList></GetRecordingSearchResultsResponse>`)
		case strings.Contains(body, "EndSearch"):
			return soapOK(`<EndSearchResponse xmlns="http://www.onvif.org/ver10/search/wsdl"><Endpoint>2026-11-17T01:00:00Z</Endpoint></EndSearchResponse>`)
		}
		return ""
	})
	svc := NewRecordingService(leftoverRecordingClient(t, server))
	filter := SearchFilter{
		RecordingToken: "cam&1", StartTime: time.Date(2026, 11, 17, 0, 30, 0, 0, time.UTC), MaxResults: 5,
	}
	result, err := svc.SearchRecordings(t.Context(), filter)
	require.NoError(t, err)
	require.True(t, result.HasMore)
	require.Len(t, result.Segments, 1)
	require.True(t, result.Segments[0].RecordingBounds)
	require.EqualValues(t, 3600, result.Segments[0].Duration)
	filter.SearchToken = result.SearchToken
	result, err = svc.SearchRecordings(t.Context(), filter)
	require.NoError(t, err)
	require.False(t, result.HasMore)
	require.NoError(t, svc.EndSearch(t.Context(), result.SearchToken))
}

func TestONVIF2606PullPointAddressingLeaseAndLongPoll(t *testing.T) {
	const wsa = "http://www.w3.org/2005/08/addressing"
	const events = "http://www.onvif.org/ver10/events/wsdl"
	var server *httptest.Server
	pollStarted := make(chan struct{})
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var env struct {
			Header struct {
				Action string `xml:"http://www.w3.org/2005/08/addressing Action"`
				To     string `xml:"http://www.w3.org/2005/08/addressing To"`
				Ref    struct {
					Marker bool   `xml:"http://www.w3.org/2005/08/addressing IsReferenceParameter,attr"`
					ID     string `xml:",chardata"`
				} `xml:"urn:vendor SubscriptionID"`
			} `xml:"Header"`
		}
		assert.NoError(t, xml.Unmarshal(body, &env))
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if r.URL.Path == "/control" {
			_, _ = io.WriteString(w, soapOK("<ControlResponse/>"))
			return
		}
		assert.Equal(t, env.Header.Action, params["action"])
		assert.Equal(t, server.URL+r.URL.RequestURI(), env.Header.To)
		var reply string
		if strings.HasSuffix(env.Header.Action, "/CreatePullPointSubscriptionRequest") {
			reply = fmt.Sprintf(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:wsa="%s" xmlns:v="urn:vendor" xmlns:tev="%s" xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2"><s:Body>
				<tev:CreatePullPointSubscriptionResponse><tev:SubscriptionReference><wsa:Address>%s/sub?channel=1&amp;x=2</wsa:Address>
					<wsa:ReferenceParameters><v:SubscriptionID>ref-123</v:SubscriptionID></wsa:ReferenceParameters></tev:SubscriptionReference>
					<wsnt:CurrentTime>2026-11-17T00:00:00Z</wsnt:CurrentTime><wsnt:TerminationTime>2026-11-17T00:00:25Z</wsnt:TerminationTime>
				</tev:CreatePullPointSubscriptionResponse></s:Body></s:Envelope>`, wsa, events, server.URL)
		} else {
			assert.True(t, env.Header.Ref.Marker)
			assert.Equal(t, "ref-123", env.Header.Ref.ID)
			switch {
			case strings.HasSuffix(env.Header.Action, "/SetSynchronizationPointRequest"):
				reply = soapOK(`<SetSynchronizationPointResponse xmlns="` + events + `"/>`)
			case strings.HasSuffix(env.Header.Action, "/PullMessagesRequest"):
				close(pollStarted)
				time.Sleep(300 * time.Millisecond)
				reply = soapOK(`<tev:PullMessagesResponse xmlns:tev="` + events + `" xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2" xmlns:tt="http://www.onvif.org/ver10/schema">
					<tev:CurrentTime>2026-11-17T00:00:01Z</tev:CurrentTime><tev:TerminationTime>2026-11-17T00:00:31Z</tev:TerminationTime>
					<wsnt:NotificationMessage><wsnt:Topic>tns1:VideoSource/MotionAlarm</wsnt:Topic><wsnt:Message>
					<tt:Message UtcTime="2026-11-17T00:00:01Z" PropertyOperation="Changed"><tt:Data><tt:SimpleItem Name="State" Value="true"/></tt:Data></tt:Message>
					</wsnt:Message></wsnt:NotificationMessage></tev:PullMessagesResponse>`)
			case strings.HasSuffix(env.Header.Action, "/RenewRequest"):
				reply = soapOK(`<RenewResponse xmlns="http://docs.oasis-open.org/wsn/b-2"><CurrentTime>2026-11-17T00:00:02Z</CurrentTime><TerminationTime>2026-11-17T00:00:42Z</TerminationTime></RenewResponse>`)
			case strings.HasSuffix(env.Header.Action, "/UnsubscribeRequest"):
				reply = soapOK(`<UnsubscribeResponse xmlns="http://docs.oasis-open.org/wsn/b-2"/>`)
			}
		}
		_, _ = io.WriteString(w, reply)
	}))
	defer server.Close()
	client := leftoverRecordingClient(t, server)
	client.soap = NewSOAPClient(&http.Client{
		Timeout:   250 * time.Millisecond,
		Transport: &http.Transport{MaxConnsPerHost: 1, ResponseHeaderTimeout: 200 * time.Millisecond},
	}, "user", "secret")
	service := NewEventsService(client)
	sub, err := service.CreatePullPointSubscription(t.Context())
	require.NoError(t, err)
	require.InDelta(t, 25, sub.Timeout.Seconds(), 0.1)
	require.NoError(t, sub.SetSynchronizationPoint(t.Context()))

	type pullResult struct {
		messages []Event
		err      error
	}
	pulled := make(chan pullResult, 1)
	go func() {
		messages, err := sub.PullMessages(t.Context(), time.Second, 5)
		pulled <- pullResult{messages, err}
	}()
	<-pollStarted
	_, err = client.soap.SendContext(t.Context(), &SOAPRequest{ServiceURL: server.URL + "/control", Body: "<Control/>"})
	require.NoError(t, err)
	select {
	case <-pulled:
		t.Fatal("control request waited for the long poll")
	default:
	}
	result := <-pulled
	require.NoError(t, result.err)
	require.Len(t, result.messages, 1)
	require.Equal(t, "true", result.messages[0].Data["State"])
	require.InDelta(t, 30, time.Until(sub.ExpiresAt()).Seconds(), 0.3)
	require.NoError(t, sub.Renew(t.Context(), time.Minute))
	require.InDelta(t, 40, time.Until(sub.ExpiresAt()).Seconds(), 0.3)
	require.NoError(t, sub.Unsubscribe(t.Context()))
}

func TestONVIF2606DiscoveryCorrelatesAndReadsAllMatches(t *testing.T) {
	document := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing">
		<s:Header><a:RelatesTo>uuid:request</a:RelatesTo><a:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches</a:Action></s:Header>
		<s:Body><d:ProbeMatches>
			<d:ProbeMatch><a:EndpointReference><a:Address>urn:uuid:first</a:Address></a:EndpointReference><d:XAddrs>http://192.0.2.1/onvif/device_service</d:XAddrs></d:ProbeMatch>
			<d:ProbeMatch><a:EndpointReference><a:Address>urn:uuid:second</a:Address></a:EndpointReference><d:XAddrs>http://192.0.2.2/onvif/device_service</d:XAddrs></d:ProbeMatch>
		</d:ProbeMatches></s:Body></s:Envelope>`
	require.Empty(t, parseDiscoveryMatches([]byte(document), "", "uuid:another"))
	matches := parseDiscoveryMatches([]byte(document), "", "uuid:request")
	require.Len(t, matches, 2)
	require.Equal(t, "urn:uuid:second", matches[1].UUID)
}
