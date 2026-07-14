package gb28181

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/emiago/sipgo/sip"
)

func randInt(min, max int) int {
	return rand.Intn(max-min+1) + min
}

func randInt64(min, max int64) int64 {
	return rand.Int63n(max-min+1) + min
}

func xmlUnmarshal(data []byte, v interface{}) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		if utf8.Valid(data) {
			return input, nil
		}
		return simplifiedchinese.GB18030.NewDecoder().Reader(input), nil
	}
	if err := decoder.Decode(v); err == nil {
		return nil
	}
	// Retry with GB2312 encoding declaration
	value := string(data)
	value = strings.Replace(value, `<?xml version="1.0"?>`, `<?xml version="1.0" encoding="GB2312"?>`, 1)
	value = strings.Replace(value, `UTF-8`, `GB2312`, 1)
	return xmlUnmarshalRetry([]byte(value), v)
}

func xmlUnmarshalRetry(data []byte, v interface{}) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		if utf8.Valid(data) {
			return input, nil
		}
		return simplifiedchinese.GB18030.NewDecoder().Reader(input), nil
	}
	return decoder.Decode(v)
}

func (g *GB28181API) sendMessage(targetID string, dev *Device, body []byte) error {
	uri := sip.Uri{
		Scheme: "sip",
		User:   targetID,
		Host:   dev.Address,
		Port:   5060,
	}
	if host, port, err := net.SplitHostPort(dev.Address); err == nil {
		uri.Host = host
		if p := portInt(port); p > 0 {
			uri.Port = p
		}
	}

	req := sip.NewRequest(sip.MESSAGE, uri)
	req.SetBody(body)
	req.AppendHeader(sip.NewHeader("Content-Type", "Application/MANSCDP+xml"))

	slog.Info("[SIP] MESSAGE sending",
		"target", targetID,
		"uri", uri.String(),
		"body_len", len(body),
	)

	_, err := g.client.Do(context.Background(), req)
	if err != nil {
		slog.Error("[SIP] MESSAGE send failed", "target", targetID, "error", err)
	} else {
		slog.Info("[SIP] MESSAGE sent success", "target", targetID)
	}
	return err
}

func portInt(s string) int {
	var p int
	fmt.Sscanf(s, "%d", &p)
	return p
}

func init() {
	rand.Seed(time.Now().UnixNano())
}
