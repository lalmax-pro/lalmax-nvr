package sdp

import "errors"

var (
	ErrUnsupportedSDP  = errors.New("voip: unsupported sdp")
	ErrUnsupportedZRTP = errors.New("voip: zrtp not implemented")
	ErrInvalidSDP      = errors.New("voip: invalid sdp")
	ErrNoMedia         = errors.New("voip: no negotiated media")
)
