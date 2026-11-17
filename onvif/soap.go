package onvif

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	soapNamespace       = "http://www.w3.org/2003/05/soap-envelope"
	addressingNamespace = "http://www.w3.org/2005/08/addressing"
	maxSOAPResponse     = 16 << 20
)

// SOAPClient handles SOAP communication with ONVIF devices.
type SOAPClient struct {
	httpClient *http.Client
	pollClient *http.Client
	origin     *url.URL
	username   string
	password   string

	mu          sync.Mutex
	timeShift   time.Duration
	digestNonce string
	digestCount uint32
	requestGate chan struct{}
}

// NewSOAPClient creates a new SOAP client.
func NewSOAPClient(httpClient *http.Client, username, password string) *SOAPClient {
	client := *httpClient
	// A redirect can replay the XML body and its credentials at another address.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	pollClient := client
	pollClient.Timeout = 0 // Long polls have an operation-specific context deadline.
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if tr, ok := transport.(*http.Transport); ok {
		pollTransport := tr.Clone()
		pollTransport.ResponseHeaderTimeout = 0
		pollClient.Transport = pollTransport
	}
	return &SOAPClient{
		httpClient:  &client,
		pollClient:  &pollClient,
		username:    username,
		password:    password,
		requestGate: make(chan struct{}, 1),
	}
}

// SetTimeShift sets the time difference with the device.
func (s *SOAPClient) SetTimeShift(shift time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timeShift = shift
}

// SOAPRequest represents a SOAP request.
type SOAPRequest struct {
	ServiceURL          string
	Action              string
	Body                string
	Namespace           string
	NoAuth              bool // Omit the initial WS-Security header.
	addressing          bool
	referenceParameters string
	pollTimeout         time.Duration
}

// SOAPResponse represents a SOAP response.
type SOAPResponse struct {
	Body     []byte
	Header   []byte
	Fault    *SOAPFault
	HTTPCode int
}

// SOAPFault represents a SOAP fault.
type SOAPFault struct {
	Code     string
	Subcodes []string
	Reason   string
	Detail   string
}

func (f *SOAPFault) Error() string {
	code := strings.Join(append([]string{f.Code}, f.Subcodes...), "/")
	return fmt.Sprintf("SOAP Fault: %s - %s", code, f.Reason)
}

func (f *SOAPFault) hasSubcode(name string) bool {
	for _, code := range f.Subcodes {
		if code == name || strings.HasSuffix(code, ":"+name) {
			return true
		}
	}
	return false
}

type soapHTTPError struct {
	status int
}

func (e *soapHTTPError) Error() string {
	if e.status == http.StatusUnauthorized {
		return "onvif: authentication failed (401)"
	}
	return fmt.Sprintf("onvif: HTTP error %d", e.status)
}

func operationUnsupported(err error) bool {
	var fault *SOAPFault
	var status *soapHTTPError
	return (errors.As(err, &fault) && (fault.hasSubcode("ActionNotSupported") || fault.hasSubcode("OptionalActionNotImplemented"))) ||
		(errors.As(err, &status) && (status.status == http.StatusNotFound || status.status == http.StatusNotImplemented))
}

// Send sends a SOAP request and returns the response.
func (s *SOAPClient) Send(req *SOAPRequest) (*SOAPResponse, error) {
	return s.SendContext(context.Background(), req)
}

// SendContext sends a SOAP request using the caller's deadline and cancellation.
func (s *SOAPClient) SendContext(ctx context.Context, req *SOAPRequest) (*SOAPResponse, error) {
	endpoint, err := resolveONVIFEndpoint(s.origin, req.ServiceURL)
	if err != nil {
		return nil, fmt.Errorf("onvif: create request failed: %w", err)
	}
	client := s.httpClient
	if req.pollTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.pollTimeout+10*time.Second)
		defer cancel()
		client = s.pollClient
	} else {
		// Many embedded cameras expect control requests to be serialized.
		select {
		case s.requestGate <- struct{}{}:
			defer func() { <-s.requestGate }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	security := ""
	if s.username != "" && !req.NoAuth {
		security = "PasswordDigest"
	}
	resp, transportResp, err := s.exchange(ctx, client, req, endpoint, security, digestChallenge{})
	if resp != nil && resp.Fault != nil && strings.HasSuffix(resp.Fault.Code, ":MustUnderstand") && security != "" {
		resp, transportResp, err = s.exchange(ctx, client, req, endpoint, "", digestChallenge{})
	}
	if transportResp == nil || transportResp.StatusCode != http.StatusUnauthorized || s.username == "" {
		return resp, err
	}

	if challenge, ok := preferredDigestChallenge(transportResp.Header.Values("WWW-Authenticate")); ok {
		resp, transportResp, err = s.exchange(ctx, client, req, endpoint, "", challenge)
		if transportResp == nil || transportResp.StatusCode != http.StatusUnauthorized {
			return resp, err
		}
		if next, ok := preferredDigestChallenge(transportResp.Header.Values("WWW-Authenticate")); ok &&
			next.params["stale"] == "true" && next.params["nonce"] != challenge.params["nonce"] {
			resp, _, err := s.exchange(ctx, client, req, endpoint, "", next)
			return resp, err
		}
	}
	// Legacy cameras may require PasswordText. Release it only over authenticated TLS.
	if endpoint.Scheme == "https" && (s.origin == nil || sameAuthority(s.origin, endpoint)) &&
		transportResp.TLS != nil && len(transportResp.TLS.VerifiedChains) > 0 &&
		len(transportResp.TLS.PeerCertificates) > 0 &&
		transportResp.TLS.PeerCertificates[0].VerifyHostname(endpoint.Hostname()) == nil {
		resp, _, err = s.exchange(ctx, client, req, endpoint, "PasswordText", digestChallenge{})
	}
	return resp, err
}

func (s *SOAPClient) exchange(ctx context.Context, client *http.Client, req *SOAPRequest, endpoint *url.URL, security string, challenge digestChallenge) (*SOAPResponse, *http.Response, error) {
	envelope := s.buildEnvelope(req, endpoint, security)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(envelope))
	if err != nil {
		return nil, nil, fmt.Errorf("onvif: create request failed: %w", err)
	}
	contentType := "application/soap+xml; charset=utf-8"
	if req.Action != "" {
		contentType = mime.FormatMediaType("application/soap+xml", map[string]string{"charset": "utf-8", "action": req.Action})
		httpReq.Header.Set("SOAPAction", req.Action) // Older cameras dispatch using this header.
	}
	httpReq.Header.Set("Content-Type", contentType)
	if challenge.params != nil {
		httpReq.Header.Set("Authorization", s.digestAuthorization(httpReq, envelope, challenge))
	}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, nil, fmt.Errorf("onvif: send request failed: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxSOAPResponse+1))
	if err != nil {
		return nil, nil, fmt.Errorf("onvif: read response failed: %w", err)
	}
	if len(body) > maxSOAPResponse {
		return nil, nil, errors.New("onvif: SOAP response exceeds size limit")
	}
	resp := &SOAPResponse{
		Body:     body,
		Header:   []byte(httpResp.Header.Get("WWW-Authenticate")),
		HTTPCode: httpResp.StatusCode,
		Fault:    parseSOAPFault(body),
	}
	if resp.Fault != nil {
		return resp, httpResp, resp.Fault
	}
	if httpResp.StatusCode != http.StatusOK {
		return resp, httpResp, &soapHTTPError{status: httpResp.StatusCode}
	}
	return resp, httpResp, nil
}

func resolveONVIFEndpoint(origin *url.URL, address string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(address))
	if err != nil || strings.TrimSpace(address) == "" {
		return nil, errors.New("invalid ONVIF service URL")
	}
	if origin != nil {
		endpoint = origin.ResolveReference(endpoint)
	}
	if (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Hostname() == "" ||
		endpoint.User != nil || endpoint.Fragment != "" {
		return nil, errors.New("invalid ONVIF HTTP(S) service URL")
	}
	if origin != nil && (!strings.EqualFold(endpoint.Hostname(), origin.Hostname()) ||
		(origin.Scheme == "https" && endpoint.Scheme != "https")) {
		return nil, errors.New("ONVIF service URL changed device host or downgraded HTTPS")
	}
	return endpoint, nil
}

func sameAuthority(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func (s *SOAPClient) buildEnvelope(req *SOAPRequest, endpoint *url.URL, security string) string {
	var buf strings.Builder
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://www.w3.org/2005/08/addressing">
<s:Header>`)
	if req.addressing {
		buf.WriteString("<a:Action>" + escapeXML(req.Action) + "</a:Action>")
		buf.WriteString("<a:To>" + escapeXML(endpoint.String()) + "</a:To>")
		buf.WriteString(req.referenceParameters)
	}
	if security != "" {
		nonce, timestamp := s.generateWSSE()
		password := s.password
		if security == "PasswordDigest" {
			password = s.passwordDigest(nonce, timestamp)
		}
		buf.WriteString(`<Security s:mustUnderstand="1" xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"><UsernameToken><Username>`)
		buf.WriteString(escapeXML(s.username))
		buf.WriteString(`</Username><Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#` + security + `">`)
		buf.WriteString(escapeXML(password))
		buf.WriteString("</Password>")
		if security == "PasswordDigest" {
			buf.WriteString(`<Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">` + base64.StdEncoding.EncodeToString(nonce) + "</Nonce>")
		}
		buf.WriteString(`<Created xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">` + timestamp + "</Created></UsernameToken></Security>")
	}
	buf.WriteString(`</s:Header><s:Body xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema">`)
	buf.WriteString(req.Body)
	buf.WriteString("</s:Body></s:Envelope>")
	return buf.String()
}

func escapeXML(value string) string {
	var buf strings.Builder
	_ = xml.EscapeText(&buf, []byte(value))
	return buf.String()
}

func (s *SOAPClient) generateWSSE() ([]byte, string) {
	nonce := make([]byte, 16)
	rand.Read(nonce)
	s.mu.Lock()
	shift := s.timeShift
	s.mu.Unlock()
	return nonce, time.Now().UTC().Add(shift).Format("2006-01-02T15:04:05.000Z")
}

func (s *SOAPClient) passwordDigest(nonce []byte, timestamp string) string {
	h := sha1.New()
	h.Write(nonce)
	h.Write([]byte(timestamp))
	h.Write([]byte(s.password))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

type soapFaultCode struct {
	Value   string         `xml:"Value"`
	Subcode *soapFaultCode `xml:"Subcode"`
}

func parseSOAPFault(body []byte) *SOAPFault {
	var fault struct {
		XMLName   xml.Name      `xml:"Fault"`
		Code      soapFaultCode `xml:"Code"`
		Reason    string        `xml:"Reason>Text"`
		DetailXML struct {
			XML string `xml:",innerxml"`
		} `xml:"Detail"`
	}
	if err := ParseResponseBody(body, &fault); err != nil || fault.Code.Value == "" {
		return nil
	}
	result := &SOAPFault{Code: fault.Code.Value, Reason: fault.Reason, Detail: fault.DetailXML.XML}
	for subcode := fault.Code.Subcode; subcode != nil; subcode = subcode.Subcode {
		result.Subcodes = append(result.Subcodes, subcode.Value)
	}
	return result
}

// ParseResponseBody decodes the first element in the SOAP body with its namespace context.
func ParseResponseBody(body []byte, v any) error {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	inEnvelope := false
	for {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("onvif: parse envelope failed: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if !inEnvelope {
			if start.Name.Local != "Envelope" || (start.Name.Space != soapNamespace && start.Name.Space != "http://schemas.xmlsoap.org/soap/envelope/" && start.Name.Space != "") {
				return errors.New("onvif: parse envelope failed: expected Envelope")
			}
			inEnvelope = true
			continue
		}
		if start.Name.Local == "Body" {
			for {
				token, err := decoder.Token()
				if err != nil {
					return fmt.Errorf("onvif: parse body failed: %w", err)
				}
				switch token := token.(type) {
				case xml.StartElement:
					if err := decoder.DecodeElement(v, &token); err != nil {
						return fmt.Errorf("onvif: parse body failed: %w", err)
					}
					return nil
				case xml.EndElement:
					return errors.New("onvif: parse body failed: empty body")
				}
			}
		}
		if err := decoder.Skip(); err != nil {
			return fmt.Errorf("onvif: parse envelope failed: %w", err)
		}
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
