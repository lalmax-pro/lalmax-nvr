package voip

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/textproto"
	"strconv"
	"strings"
	"sync/atomic"
)

type Method string

const (
	MethodRegister Method = "REGISTER"
	MethodInvite   Method = "INVITE"
	MethodUpdate   Method = "UPDATE"
	MethodAck      Method = "ACK"
	MethodBye      Method = "BYE"
	MethodCancel   Method = "CANCEL"
	MethodOptions  Method = "OPTIONS"
)

type Message struct {
	IsRequest   bool
	Method      Method
	RequestURI  string
	StatusCode  int
	StatusText  string
	SipVersion  string
	Headers     map[string][]string
	Body        string
	RawMessage  string
	ViaReceived string // extracted from first Via for reply
	ViaRport    int
}

func ParseMessage(raw string) (*Message, error) {
	if raw == "" {
		return nil, fmt.Errorf("empty message")
	}

	msg := &Message{
		Headers:    make(map[string][]string),
		RawMessage: raw,
		SipVersion: "SIP/2.0",
	}

	parts := strings.Split(raw, "\r\n\r\n")
	headersPart := parts[0]
	if len(parts) > 1 {
		msg.Body = strings.Join(parts[1:], "\r\n\r\n")
	}

	lines := strings.Split(headersPart, "\r\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("no lines in message")
	}

	// Parse first line
	firstLine := lines[0]
	if strings.HasPrefix(firstLine, "SIP/") {
		// Response
		msg.IsRequest = false
		fields := strings.SplitN(firstLine, " ", 3)
		if len(fields) < 3 {
			return nil, fmt.Errorf("invalid response line: %s", firstLine)
		}
		code, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("invalid status code: %s", fields[1])
		}
		msg.StatusCode = code
		msg.StatusText = fields[2]
	} else {
		// Request
		msg.IsRequest = true
		fields := strings.SplitN(firstLine, " ", 3)
		if len(fields) < 3 {
			return nil, fmt.Errorf("invalid request line: %s", firstLine)
		}
		msg.Method = Method(fields[0])
		msg.RequestURI = fields[1]
	}

	// Parse headers
	previousHeader := ""
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if line == "" {
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && previousHeader != "" {
			values := msg.Headers[previousHeader]
			values[len(values)-1] += " " + strings.TrimSpace(line)
			continue
		}

		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}

		name := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])

		// Normalize compact header names
		name = normalizeHeaderName(name)

		msg.Headers[name] = append(msg.Headers[name], value)
		previousHeader = name
	}

	return msg, nil
}

func normalizeHeaderName(name string) string {
	switch strings.ToLower(name) {
	case "i", "call-id":
		return "Call-ID"
	case "m", "contact":
		return "Contact"
	case "f", "from":
		return "From"
	case "t", "to":
		return "To"
	case "v", "via":
		return "Via"
	case "l", "content-length":
		return "Content-Length"
	case "c", "content-type":
		return "Content-Type"
	case "cseq":
		return "CSeq"
	case "www-authenticate":
		return "WWW-Authenticate"
	case "min-se":
		return "Min-SE"
	default:
		return textproto.CanonicalMIMEHeaderKey(name)
	}
}

func (m *Message) GetHeader(name string) string {
	vals := m.Headers[normalizeHeaderName(name)]
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func (m *Message) GetHeaderAll(name string) []string {
	return m.Headers[normalizeHeaderName(name)]
}

func (m *Message) SetHeader(name, value string) {
	m.Headers[normalizeHeaderName(name)] = []string{value}
}

func (m *Message) AddHeader(name, value string) {
	name = normalizeHeaderName(name)
	m.Headers[name] = append(m.Headers[name], value)
}

func (m *Message) CallID() string {
	return m.GetHeader("Call-ID")
}

func (m *Message) CSeq() string {
	return m.GetHeader("CSeq")
}

func (m *Message) From() string {
	return m.GetHeader("From")
}

func (m *Message) To() string {
	return m.GetHeader("To")
}

func (m *Message) Via() []string {
	return m.GetHeaderAll("Via")
}

func (m *Message) Contact() string {
	return m.GetHeader("Contact")
}

func (m *Message) ContentType() string {
	return m.GetHeader("Content-Type")
}

func (m *Message) ContentLength() int {
	lenStr := m.GetHeader("Content-Length")
	if lenStr == "" {
		return len(m.Body)
	}
	n, _ := strconv.Atoi(lenStr)
	return n
}

// ExtractTag extracts tag from From or To header
func ExtractTag(header string) string {
	idx := strings.Index(header, "tag=")
	if idx < 0 {
		return ""
	}
	tag := header[idx+4:]
	semicolon := strings.Index(tag, ";")
	if semicolon >= 0 {
		tag = tag[:semicolon]
	}
	return strings.TrimSpace(tag)
}

// ExtractUser extracts username from From or To URI
func ExtractUser(header string) string {
	start := strings.Index(header, "sip:")
	if start < 0 {
		start = strings.Index(header, "SIP:")
	}
	if start < 0 {
		return ""
	}
	start += 4
	rest := header[start:]
	atIdx := strings.Index(rest, "@")
	if atIdx < 0 {
		// No @ sign, use until > or ;
		gtIdx := strings.Index(rest, ">")
		scIdx := strings.Index(rest, ";")
		end := len(rest)
		if gtIdx >= 0 && gtIdx < end {
			end = gtIdx
		}
		if scIdx >= 0 && scIdx < end {
			end = scIdx
		}
		return rest[:end]
	}
	return rest[:atIdx]
}

// ExtractCSeqNumber extracts the sequence number from CSeq header
func ExtractCSeqNumber(cseq string) int {
	fields := strings.Fields(cseq)
	if len(fields) < 1 {
		return 0
	}
	num, _ := strconv.Atoi(fields[0])
	return num
}

// ExtractCSeqMethod extracts the method from CSeq header
func ExtractCSeqMethod(cseq string) Method {
	fields := strings.Fields(cseq)
	if len(fields) < 2 {
		return ""
	}
	return Method(fields[1])
}

// BuildResponse creates a SIP response message
func BuildResponse(req *Message, statusCode int, statusText string, body string) string {
	var sb strings.Builder

	// Status line
	sb.WriteString(fmt.Sprintf("SIP/2.0 %d %s\r\n", statusCode, statusText))

	// Via headers (copy from request)
	for _, via := range req.Via() {
		sb.WriteString(fmt.Sprintf("Via: %s\r\n", via))
	}

	// From (copy from request)
	sb.WriteString(fmt.Sprintf("From: %s\r\n", req.From()))

	// To (copy from request, may add tag)
	to := req.To()
	if !strings.Contains(to, "tag=") && statusCode >= 200 {
		// Add tag for final responses
		to = fmt.Sprintf("%s;tag=%s", to, generateTag())
	}
	sb.WriteString(fmt.Sprintf("To: %s\r\n", to))

	// Call-ID (copy from request)
	sb.WriteString(fmt.Sprintf("Call-ID: %s\r\n", req.CallID()))

	// CSeq (copy from request)
	sb.WriteString(fmt.Sprintf("CSeq: %s\r\n", req.CSeq()))

	// Server header
	sb.WriteString("Server: lalmax-nvr-voip\r\n")

	// Content headers
	if body != "" {
		sb.WriteString(fmt.Sprintf("Content-Type: application/sdp\r\n"))
		sb.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(body)))
	} else {
		sb.WriteString("Content-Length: 0\r\n")
	}

	// Empty line
	sb.WriteString("\r\n")

	// Body
	if body != "" {
		sb.WriteString(body)
	}

	return sb.String()
}

// BuildResponseWithToTag creates a SIP response with a specific To tag
func BuildResponseWithToTag(req *Message, statusCode int, statusText string, body string, toTag string) string {
	var sb strings.Builder

	// Status line
	sb.WriteString(fmt.Sprintf("SIP/2.0 %d %s\r\n", statusCode, statusText))

	// Via headers (copy from request)
	for _, via := range req.Via() {
		sb.WriteString(fmt.Sprintf("Via: %s\r\n", via))
	}

	// From (copy from request)
	sb.WriteString(fmt.Sprintf("From: %s\r\n", req.From()))

	// To (copy from request, add specific tag)
	to := req.To()
	if !strings.Contains(to, "tag=") && toTag != "" {
		to = fmt.Sprintf("%s;tag=%s", to, toTag)
	}
	sb.WriteString(fmt.Sprintf("To: %s\r\n", to))

	// Call-ID (copy from request)
	sb.WriteString(fmt.Sprintf("Call-ID: %s\r\n", req.CallID()))

	// CSeq (copy from request)
	sb.WriteString(fmt.Sprintf("CSeq: %s\r\n", req.CSeq()))

	// Server header
	sb.WriteString("Server: lalmax-nvr-voip\r\n")

	// Content headers
	if body != "" {
		sb.WriteString(fmt.Sprintf("Content-Type: application/sdp\r\n"))
		sb.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(body)))
	} else {
		sb.WriteString("Content-Length: 0\r\n")
	}

	// Empty line
	sb.WriteString("\r\n")

	// Body
	if body != "" {
		sb.WriteString(body)
	}

	return sb.String()
}

// BuildResponseWithContact creates a SIP response with To tag and Contact header
func BuildResponseWithContact(req *Message, statusCode int, statusText string, body string, toTag string, contactURI string) string {
	var sb strings.Builder

	// Status line
	sb.WriteString(fmt.Sprintf("SIP/2.0 %d %s\r\n", statusCode, statusText))

	// Via headers (copy from request)
	for _, via := range req.Via() {
		sb.WriteString(fmt.Sprintf("Via: %s\r\n", via))
	}

	// From (copy from request)
	sb.WriteString(fmt.Sprintf("From: %s\r\n", req.From()))

	// To (copy from request, add specific tag)
	to := req.To()
	if !strings.Contains(to, "tag=") && toTag != "" {
		to = fmt.Sprintf("%s;tag=%s", to, toTag)
	}
	sb.WriteString(fmt.Sprintf("To: %s\r\n", to))

	// Call-ID (copy from request)
	sb.WriteString(fmt.Sprintf("Call-ID: %s\r\n", req.CallID()))

	// CSeq (copy from request)
	sb.WriteString(fmt.Sprintf("CSeq: %s\r\n", req.CSeq()))

	// Contact header (required for 2xx responses to INVITE)
	if contactURI != "" {
		sb.WriteString(fmt.Sprintf("Contact: %s\r\n", contactURI))
	}

	// Server header
	sb.WriteString("Server: lalmax-nvr-voip\r\n")

	// Content headers
	if body != "" {
		sb.WriteString(fmt.Sprintf("Content-Type: application/sdp\r\n"))
		sb.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(body)))
	} else {
		sb.WriteString("Content-Length: 0\r\n")
	}

	// Empty line
	sb.WriteString("\r\n")

	// Body
	if body != "" {
		sb.WriteString(body)
	}

	return sb.String()
}

// BuildRequest creates a SIP request message
func BuildRequest(method Method, uri string, headers map[string]string, body string) string {
	var sb strings.Builder

	// Request line
	sb.WriteString(fmt.Sprintf("%s %s SIP/2.0\r\n", method, uri))

	// Headers
	for name, value := range headers {
		sb.WriteString(fmt.Sprintf("%s: %s\r\n", name, value))
	}

	// Content headers
	if body != "" {
		if _, ok := headers["Content-Type"]; !ok {
			sb.WriteString("Content-Type: application/sdp\r\n")
		}
		sb.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(body)))
	} else {
		if _, ok := headers["Content-Length"]; !ok {
			sb.WriteString("Content-Length: 0\r\n")
		}
	}

	// Empty line
	sb.WriteString("\r\n")

	// Body
	if body != "" {
		sb.WriteString(body)
	}

	return sb.String()
}

func generateTag() string {
	var tag [16]byte
	if _, err := rand.Read(tag[:]); err == nil {
		return hex.EncodeToString(tag[:])
	}
	return fmt.Sprintf("%d", fallbackTag.Add(1))
}
func generateBranch() string { return "z9hG4bK-" + generateTag() }

var fallbackTag atomic.Uint64
