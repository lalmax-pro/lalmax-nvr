package onvif

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSOAPClientInvalidURLAndNetwork(t *testing.T) {
	client := NewSOAPClient(http.DefaultClient, "admin", "secret")
	_, err := client.Send(&SOAPRequest{ServiceURL: "://bad", Body: `<Get/>`})
	require.ErrorContains(t, err, "create request failed")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	_, err = client.Send(&SOAPRequest{ServiceURL: server.URL, Body: `<Get/>`})
	require.ErrorContains(t, err, "send request failed")
}

func TestSOAPClientDigestNoQOPSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Digest ") {
			w.Header().Set("WWW-Authenticate", `Digest realm="cam", nonce="n1"`)
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/soap+xml")
		_, _ = w.Write([]byte(soapOK(`<GetDeviceInformationResponse><Manufacturer>Ok</Manufacturer></GetDeviceInformationResponse>`)))
	}))
	t.Cleanup(server.Close)

	client := NewSOAPClient(server.Client(), "admin", "secret")
	resp, err := client.Send(&SOAPRequest{
		ServiceURL: server.URL + "/onvif/device_service",
		Action:     "http://www.onvif.org/ver10/device/wsdl/GetDeviceInformation",
		Body:       `<GetDeviceInformation xmlns="http://www.onvif.org/ver10/device/wsdl"/>`,
	})
	require.NoError(t, err)
	require.Contains(t, string(resp.Body), "Ok")
}

func TestSOAPClientDigest401LongBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Digest ") {
			w.Header().Set("WWW-Authenticate", `Digest realm="cam", nonce="n1", qop="auth", opaque="op", algorithm=MD5`)
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(strings.Repeat("x", 300)))
	}))
	t.Cleanup(server.Close)

	client := NewSOAPClient(server.Client(), "admin", "secret")
	_, err := client.Send(&SOAPRequest{ServiceURL: server.URL, Action: "Act", Body: `<Get/>`})
	require.Error(t, err)
}

func TestSOAPClientPasswordTextHTTPErrorAndFault(t *testing.T) {
	badGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "PasswordText") {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		http.Error(w, "auth", http.StatusUnauthorized)
	}))
	t.Cleanup(badGateway.Close)
	client := NewSOAPClient(badGateway.Client(), "admin", "secret")
	_, err := client.Send(&SOAPRequest{ServiceURL: badGateway.URL, Body: `<Get/>`})
	require.ErrorContains(t, err, "HTTP error 502")

	fault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "PasswordText") {
			w.Header().Set("Content-Type", "application/soap+xml")
			_, _ = w.Write([]byte(soapFaultXML("s:Sender", "denied")))
			return
		}
		http.Error(w, "auth", http.StatusUnauthorized)
	}))
	t.Cleanup(fault.Close)
	client = NewSOAPClient(fault.Client(), "admin", "secret")
	_, err = client.Send(&SOAPRequest{ServiceURL: fault.URL, Body: `<Get/>`})
	require.ErrorContains(t, err, "SOAP Fault")
}

func TestSOAPClientPasswordTextUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	client := NewSOAPClient(server.Client(), "admin", "secret")
	_, err := client.Send(&SOAPRequest{ServiceURL: server.URL, Body: `<Get/>`})
	require.ErrorContains(t, err, "password text auth failed (401)")
}

func TestSOAPClientInvalidDigestFallsBackToPasswordText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "PasswordText") {
			w.Header().Set("Content-Type", "application/soap+xml")
			_, _ = w.Write([]byte(soapOK(`<GetDeviceInformationResponse><Manufacturer>Text</Manufacturer></GetDeviceInformationResponse>`)))
			return
		}
		w.Header().Set("WWW-Authenticate", `Digest nonce="n1"`)
		http.Error(w, "auth", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	client := NewSOAPClient(server.Client(), "admin", "secret")
	resp, err := client.Send(&SOAPRequest{ServiceURL: server.URL, Body: `<Get/>`})
	require.NoError(t, err)
	require.Contains(t, string(resp.Body), "Text")
}

func TestParseResponseBodyInnerXMLError(t *testing.T) {
	var got struct {
		XMLName xml.Name `xml:"GetDeviceInformationResponse"`
	}
	err := ParseResponseBody([]byte(soapOK(`<WrongRoot/>`)), &got)
	require.ErrorContains(t, err, "parse body failed")
}
