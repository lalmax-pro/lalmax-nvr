package udpts

import (
	"net/url"
	"strconv"
)

const (
	programQueryKey      = "program"
	programQueryKeyAlt   = "program_id"
	programQueryKeyPN    = "pn"
	maxMPEGProgramNumber = 0xFFFF
)

// ParseProgramID 从 udp URL 读取 MPEG-TS program_number。
// 支持 query：program、program_id、pn。0 表示自适应。
func ParseProgramID(rawURL string) uint16 {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0
	}
	q := u.Query()
	for _, key := range []string{programQueryKey, programQueryKeyAlt, programQueryKeyPN} {
		if v := q.Get(key); v != "" {
			n, err := strconv.Atoi(v)
			if err == nil && n > 0 && n <= maxMPEGProgramNumber {
				return uint16(n)
			}
		}
	}
	return 0
}

// SetURLProgramID 把 program_number 写入 URL query（program=）。
func SetURLProgramID(rawURL string, programID uint16) string {
	if programID == 0 {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set(programQueryKey, strconv.Itoa(int(programID)))
	u.RawQuery = q.Encode()
	return u.String()
}

// ApplyProgramID 在 JSON program_id > 0 时覆盖 URL 中的 program。
func ApplyProgramID(rawURL string, programID int) string {
	if programID <= 0 || programID > maxMPEGProgramNumber {
		return rawURL
	}
	return SetURLProgramID(rawURL, uint16(programID))
}
