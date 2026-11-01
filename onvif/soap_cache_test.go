package onvif

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func soapOK(body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
<s:Body>` + body + `</s:Body>
</s:Envelope>`
}

func soapFaultXML(code, reason string) string {
	return soapOK(`<s:Fault><s:Code><s:Value>` + code + `</s:Value></s:Code><s:Reason><s:Text>` + reason + `</s:Text></s:Reason></s:Fault>`)
}

func TestSOAPClientDigestThenPasswordText(t *testing.T) {
	var digestHits, textHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		auth := r.Header.Get("Authorization")
		switch {
		case auth == "":
			if bytes.Contains(body, []byte("PasswordText")) {
				textHits.Add(1)
				w.Header().Set("Content-Type", "application/soap+xml")
				_, _ = w.Write([]byte(soapOK(`<GetDeviceInformationResponse><Manufacturer>Stub</Manufacturer></GetDeviceInformationResponse>`)))
				return
			}
			w.Header().Set("WWW-Authenticate", `Digest realm="camera", nonce="abc123", qop="auth", opaque="op", algorithm=MD5`)
			http.Error(w, "auth", http.StatusUnauthorized)
		case strings.HasPrefix(auth, "Digest "):
			digestHits.Add(1)
			http.Error(w, "still unauthorized", http.StatusUnauthorized)
		default:
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	client := NewSOAPClient(server.Client(), "admin", "secret")
	client.SetTimeShift(2 * time.Second)
	resp, err := client.Send(&SOAPRequest{
		ServiceURL: server.URL + "/onvif/device_service",
		Action:     "http://www.onvif.org/ver10/device/wsdl/GetDeviceInformation",
		Body:       `<GetDeviceInformation xmlns="http://www.onvif.org/ver10/device/wsdl"/>`,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.HTTPCode)
	require.Equal(t, int32(1), digestHits.Load())
	require.Equal(t, int32(1), textHits.Load())
	require.Contains(t, string(resp.Body), "Stub")
}

func TestSOAPClientFaultAndHTTPErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fault":
			w.Header().Set("Content-Type", "application/soap+xml")
			_, _ = w.Write([]byte(soapFaultXML("s:Sender", "not authorized")))
		case "/http500":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/unauth":
			http.Error(w, "nope", http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	authed := NewSOAPClient(server.Client(), "admin", "secret")
	_, err := authed.Send(&SOAPRequest{ServiceURL: server.URL + "/fault", Body: `<Get/>`})
	require.ErrorContains(t, err, "SOAP Fault")
	var fault *SOAPFault
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "s:Sender", fault.Code)
	require.Equal(t, "not authorized", fault.Reason)

	_, err = authed.Send(&SOAPRequest{ServiceURL: server.URL + "/http500", Body: `<Get/>`})
	require.ErrorContains(t, err, "HTTP error 500")

	anon := NewSOAPClient(server.Client(), "", "")
	_, err = anon.Send(&SOAPRequest{ServiceURL: server.URL + "/unauth", Body: `<Get/>`})
	require.ErrorContains(t, err, "authentication failed (401)")
}

func TestParseDigestChallengeAndPasswordDigest(t *testing.T) {
	realm, nonce, qop, opaque, algorithm := parseDigestChallenge(`Digest realm="office", nonce="n1", qop="auth", opaque="o1", algorithm="MD5", extra`)
	require.Equal(t, "office", realm)
	require.Equal(t, "n1", nonce)
	require.Equal(t, "auth", qop)
	require.Equal(t, "o1", opaque)
	require.Equal(t, "MD5", algorithm)
	realm, _, _, _, _ = parseDigestChallenge("Basic abc")
	require.Empty(t, realm)

	client := NewSOAPClient(http.DefaultClient, "user", "pass")
	digest := client.passwordDigest([]byte("nonce"), "2026-01-02T03:04:05.000Z")
	h := sha1.New()
	h.Write([]byte("nonce"))
	h.Write([]byte("2026-01-02T03:04:05.000Z"))
	h.Write([]byte("pass"))
	require.Equal(t, base64.StdEncoding.EncodeToString(h.Sum(nil)), digest)
	require.Len(t, randomHex(8), 16)
	require.Equal(t, "900150983cd24fb0d6963f7d28e17f72", md5Sum("abc"))
}

func TestParseResponseBodyAndFormatPrefixMask(t *testing.T) {
	var got struct {
		XMLName      xml.Name `xml:"GetDeviceInformationResponse"`
		Manufacturer string   `xml:"Manufacturer"`
	}
	require.NoError(t, ParseResponseBody([]byte(soapOK(`<GetDeviceInformationResponse><Manufacturer>Hik</Manufacturer></GetDeviceInformationResponse>`)), &got))
	require.Equal(t, "Hik", got.Manufacturer)
	require.Error(t, ParseResponseBody([]byte("not xml"), &got))
	require.Equal(t, "", formatPrefixMask(0))
	require.Equal(t, "", formatPrefixMask(33))
	require.Equal(t, "ffffff00", formatPrefixMask(24))
	require.Equal(t, "ffffff80", formatPrefixMask(25))
	require.Equal(t, "onvif-cam", extractScopeValue([]string{"onvif://www.onvif.org/name/onvif-cam"}, "name"))
	require.Empty(t, extractScopeValue([]string{"onvif://www.onvif.org/hardware/IPC"}, "name"))
}

func TestONVIFCacheLifecycle(t *testing.T) {
	cache := NewCache()
	cache.Set("profiles", []string{"main"}, time.Hour)
	got, ok := cache.Get("profiles")
	require.True(t, ok)
	require.Equal(t, []string{"main"}, got)
	_, ok = cache.Get("missing")
	require.False(t, ok)

	cache.Set("stale", 1, -time.Second)
	_, ok = cache.Get("stale")
	require.False(t, ok)
	cache.Set("soon", 2, -time.Millisecond)
	cache.Cleanup()
	_, ok = cache.Get("soon")
	require.False(t, ok)

	cache.Set("keep", "value", time.Hour)
	cache.Delete("keep")
	_, ok = cache.Get("keep")
	require.False(t, ok)
	cache.Set("other", 3, time.Hour)
	cache.Clear()
	_, ok = cache.Get("other")
	require.False(t, ok)

	ctx, cancel := context.WithCancel(context.Background())
	cache.StartCleanup(ctx, 10*time.Millisecond)
	cache.Set("bg", 4, -time.Millisecond)
	time.Sleep(25 * time.Millisecond)
	cancel()
	_, ok = cache.Get("bg")
	require.False(t, ok)
}

func TestClientPoolReuseAndInvalidEndpoint(t *testing.T) {
	pool := NewClientPool()
	require.Equal(t, 0, pool.Size())
	_, err := pool.GetClient("://bad", "admin", "secret")
	require.ErrorContains(t, err, "create client failed")

	ready := &Client{ready: true, endpoint: "http://127.0.0.1/onvif"}
	pool.clients["admin@http://cam/onvif"] = ready
	got, err := pool.GetClient("http://cam/onvif", "admin", "secret")
	require.NoError(t, err)
	require.Same(t, ready, got)
	require.Equal(t, 1, pool.Size())
	pool.RemoveClient("http://cam/onvif", "admin")
	require.Equal(t, 0, pool.Size())
	pool.clients["admin@http://cam/onvif"] = ready
	pool.Clear()
	require.Equal(t, 0, pool.Size())
}

func TestNewClientOptions(t *testing.T) {
	_, err := NewClient("://bad", "u", "p")
	require.ErrorContains(t, err, "invalid endpoint")

	client, err := NewClient("https://camera.example/onvif/device_service", "u", "p", WithTimeout(2*time.Second))
	require.NoError(t, err)
	require.Equal(t, 443, client.port)
	require.True(t, client.useTLS)
	require.Equal(t, 2*time.Second, client.httpClient.Timeout)
	require.False(t, client.IsReady())
	require.Nil(t, client.Endpoints())
}
