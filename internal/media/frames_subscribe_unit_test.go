package media

import (
	"context"
	"testing"
	"time"

	"github.com/q191201771/lal/pkg/base"
	"github.com/stretchr/testify/require"
)

func newUnitFrameSub(rawSize, frameSize int) *embeddedFrameSub {
	return &embeddedFrameSub{raw: make(chan base.RtmpMsg, rawSize), frames: make(chan MediaFrame, frameSize), errc: make(chan error, 1)}
}

func TestEmbeddedFrameSubClonesDropsAndCloses(t *testing.T) {
	s := newUnitFrameSub(1, 1)
	msg := base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio}, Payload: []byte{0xa0, 1, 0x21}}
	s.OnMsg(msg)
	msg.Payload[2] = 0x99
	s.OnMsg(msg) // A full raw queue must not block a publisher.
	require.Len(t, s.raw, 1)
	require.Equal(t, byte(0x21), (<-s.raw).Payload[2])
	require.NoError(t, s.Close())
	require.NoError(t, s.Close())
	s.OnMsg(msg) // Sending after close must not panic.
	_, ok := <-s.raw
	require.False(t, ok)
	require.Empty(t, s.Err())
	require.Equal(t, (<-chan MediaFrame)(s.frames), s.Frames())
}

func TestEmbeddedFrameSubStopAndConversion(t *testing.T) {
	s := newUnitFrameSub(3, 1)
	msg := base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio}, Payload: []byte{0xa0, 1, 0x21}}
	s.OnMsg(msg)
	s.OnMsg(msg)
	s.OnMsg(base.RtmpMsg{}) // Unsupported messages yield no frames.
	s.OnStop()
	s.OnStop()
	require.NoError(t, s.Close())
	require.Len(t, s.Err(), 1)
	require.ErrorContains(t, <-s.Err(), "media stream stopped")
	s.convertLoop() // No consumer: conversion must drop frames rather than block.
	frames := s.Frames()
	got, ok := <-frames
	require.True(t, ok)
	require.Equal(t, "aac", got.Codec)
	require.Equal(t, []byte{0x21}, got.Data)
	_, ok = <-frames
	require.False(t, ok)
}

func TestEmbeddedFrameSubStopWithFullErrorQueue(t *testing.T) {
	s := newUnitFrameSub(0, 0)
	s.errc <- context.Canceled
	s.OnStop()
	require.ErrorIs(t, <-s.Err(), context.Canceled)
	require.True(t, s.closed.Load())
}

func TestEmbeddedSubscribeFramesValidationAndCancelledWait(t *testing.T) {
	e := &EmbeddedLalmax{}
	_, err := e.SubscribeFrames(context.Background(), SubscribeFramesRequest{})
	require.ErrorContains(t, err, "stream ID is required")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = e.SubscribeFrames(ctx, SubscribeFramesRequest{StreamID: "unit-absent-stream-74", AppName: "unit-app"})
	require.ErrorIs(t, err, context.Canceled)
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err = waitLalmaxGroup(ctx, "unit-absent-stream-74", "live")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
