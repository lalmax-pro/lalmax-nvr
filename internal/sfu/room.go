// Package sfu defines the NVR-facing room and track management contract.
package sfu

import "errors"

var (
	ErrInvalidID     = errors.New("invalid room ID")
	ErrRoomExists    = errors.New("room already exists")
	ErrRoomNotFound  = errors.New("room not found")
	ErrTrackNotFound = errors.New("track not found")
)

// Room contains only the management information exposed by the NVR API.
type Room struct {
	ID           string `json:"id"`
	Generation   string `json:"generation"`
	Participants int    `json:"participants"`
}
