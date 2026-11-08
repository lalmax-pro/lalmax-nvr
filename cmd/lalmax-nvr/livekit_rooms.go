package main

import (
	"context"
	"sort"
	"sync"

	"github.com/lalmax-pro/lalmax-nvr/internal/sfu"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
	"github.com/livekit/livekit-server/pkg/rtc/types"
	"github.com/livekit/livekit-server/pkg/service"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// nvrLiveKitRoomAdmin delegates room state to LiveKit and applies NVR policy.
type nvrLiveKitRoomAdmin struct {
	runtime *nvrLiveKitRuntime
	mu      sync.Mutex
}

func (a *nvrLiveKitRoomAdmin) server(ctx context.Context, principal string) (*service.LivekitServer, error) {
	if ctx == nil || a.runtime.policy.check(ctx, principal) != nil {
		return nil, sfubridge.ErrDenied
	}
	a.runtime.mu.Lock()
	server, closed := a.runtime.server, a.runtime.closed
	a.runtime.mu.Unlock()
	if closed || server == nil || !server.IsRunning() {
		return nil, sfubridge.ErrClosed
	}
	return server, nil
}

func (a *nvrLiveKitRoomAdmin) ListRooms(ctx context.Context, principal string) ([]sfu.Room, error) {
	server, err := a.server(ctx, principal)
	if err != nil {
		return nil, err
	}
	client := lksdk.NewRoomServiceClient(liveKitLoopbackURL(a.runtime, server.HTTPPort()), a.runtime.apiKey, a.runtime.config.Keys[a.runtime.apiKey])
	rooms, err := client.ListRooms(ctx, &livekit.ListRoomsRequest{})
	if err != nil {
		return nil, err
	}
	result := make([]sfu.Room, 0, len(rooms.Rooms))
	for _, room := range rooms.Rooms {
		if !validNVRSFURoomID(room.Name) {
			continue
		}
		current := server.RoomManager().GetRoom(ctx, livekit.RoomName(room.Name))
		if current == nil || string(current.ID()) != room.Sid {
			continue
		}
		snapshot := sfu.Room{ID: room.Name, Generation: room.Sid}
		for _, participant := range current.GetParticipants() {
			if !participant.ToProto().GetPermission().GetHidden() {
				snapshot.Participants++
			}
		}
		result = append(result, snapshot)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (a *nvrLiveKitRoomAdmin) CreateRoom(ctx context.Context, principal, id string) (sfu.Room, error) {
	if !validNVRSFURoomID(id) {
		return sfu.Room{}, sfu.ErrInvalidID
	}
	server, err := a.server(ctx, principal)
	if err != nil {
		return sfu.Room{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if server.RoomManager().GetRoom(ctx, livekit.RoomName(id)) != nil {
		return sfu.Room{}, sfu.ErrRoomExists
	}
	marker, err := secureBridgeID()
	if err != nil {
		return sfu.Room{}, err
	}
	created, err := server.RoomManager().CreateRoom(ctx, &livekit.CreateRoomRequest{Name: id, Metadata: marker})
	if err != nil {
		return sfu.Room{}, err
	}
	if created.Metadata != marker {
		return sfu.Room{}, sfu.ErrRoomExists
	}
	room := server.RoomManager().GetRoom(ctx, livekit.RoomName(id))
	if room == nil || string(room.ID()) != created.Sid {
		return sfu.Room{}, sfu.ErrRoomNotFound
	}
	room.SetMetadata("")
	if _, err = a.server(ctx, principal); err != nil {
		room.Close(types.RoomCloseReasonAPIDelete)
		return sfu.Room{}, err
	}
	return sfu.Room{ID: id, Generation: created.Sid}, nil
}

func (a *nvrLiveKitRoomAdmin) CloseRoom(ctx context.Context, principal, id, generation string) error {
	if !validNVRSFURoomID(id) || id == "default" || generation == "" {
		return sfu.ErrInvalidID
	}
	server, err := a.server(ctx, principal)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	room := server.RoomManager().GetRoom(ctx, livekit.RoomName(id))
	if room == nil || string(room.ID()) != generation {
		return sfu.ErrRoomNotFound
	}
	room.Close(types.RoomCloseReasonAPIDelete)
	return nil
}
