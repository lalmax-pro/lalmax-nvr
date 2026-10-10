package voip

import "errors"

var (
	ErrUnsupportedSDP = errors.New("voip: unsupported sdp")
	ErrInvalidSDP     = errors.New("voip: invalid sdp")
	ErrNoMedia        = errors.New("voip: no negotiated media")
)
