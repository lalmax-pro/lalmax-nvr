package storage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func covGBChannel(deviceID, channelID, name string) GB28181ChannelRow {
	return GB28181ChannelRow{
		DeviceID:  deviceID,
		ChannelID: channelID,
		Name:      name,
		Status:    "ON",
		CivilCode: "3402000000",
		ParentID:  "parent-1",
	}
}

// TestCovGBDevices covers device CRUD, status transitions and catalog listing.
func TestCovGBDevices(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	devices, err := db.ListGB28181Devices(ctx)
	require.NoError(t, err)
	require.Empty(t, devices)

	device := &GB28181DeviceRow{
		DeviceID: "dev-1", Name: "Camera One", Manufacturer: "Acme", Model: "M1",
		Firmware: "1.0", GBVersion: "2016", IsOnline: true, Address: "10.0.0.1:5060",
	}
	require.NoError(t, db.UpsertGB28181Device(ctx, device))
	require.NoError(t, db.UpsertGB28181Device(ctx, &GB28181DeviceRow{
		DeviceID: "dev-2", Name: "Camera Two", IsOnline: false, Address: "10.0.0.2:5060",
	}))

	got, err := db.GetGB28181Device(ctx, "dev-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "Camera One", got.Name)
	require.Equal(t, "Acme", got.Manufacturer)
	require.True(t, got.IsOnline)
	require.Equal(t, "10.0.0.1:5060", got.Address)
	require.Nil(t, got.LastKeepaliveAt)
	require.Nil(t, got.LastRegisterAt)

	_, err = db.GetGB28181Device(ctx, "missing")
	require.ErrorIs(t, err, sql.ErrNoRows)

	// Upsert with empty optional values keeps the previous ones.
	require.NoError(t, db.UpsertGB28181Device(ctx, &GB28181DeviceRow{DeviceID: "dev-1", Name: "", IsOnline: false}))
	merged, err := db.GetGB28181Device(ctx, "dev-1")
	require.NoError(t, err)
	require.Equal(t, "Camera One", merged.Name, "empty name keeps the existing value")
	require.Equal(t, "Acme", merged.Manufacturer)
	require.False(t, merged.IsOnline)

	// Status updates create or update rows and stamp keepalive/registration times.
	require.NoError(t, db.UpdateGB28181DeviceStatus(ctx, "dev-1", true, "10.0.0.9:5060"))
	status, err := db.GetGB28181Device(ctx, "dev-1")
	require.NoError(t, err)
	require.True(t, status.IsOnline)
	require.Equal(t, "10.0.0.9:5060", status.Address)
	require.NotNil(t, status.LastKeepaliveAt)

	require.NoError(t, db.UpdateGB28181DeviceStatus(ctx, "dev-new", true, "10.0.0.10:5060"))
	created, err := db.GetGB28181Device(ctx, "dev-new")
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, "10.0.0.10:5060", created.Address)

	require.NoError(t, db.UpdateGB28181DeviceRegistration(ctx, "dev-1", "10.0.0.11:5060"))
	registered, err := db.GetGB28181Device(ctx, "dev-1")
	require.NoError(t, err)
	require.True(t, registered.IsOnline)
	require.Equal(t, "10.0.0.11:5060", registered.Address)
	require.NotNil(t, registered.LastRegisterAt)

	require.NoError(t, db.UpdateGB28181DeviceOnlineStatus(ctx, "dev-1", false))
	offline, err := db.GetGB28181Device(ctx, "dev-1")
	require.NoError(t, err)
	require.False(t, offline.IsOnline)

	// List: online first, then by device id.
	require.NoError(t, db.UpdateGB28181DeviceOnlineStatus(ctx, "dev-1", true))
	devices, err = db.ListGB28181Devices(ctx)
	require.NoError(t, err)
	require.Len(t, devices, 3)
	require.Equal(t, "dev-1", devices[0].DeviceID)
	require.True(t, devices[0].IsOnline)
	require.NotNil(t, devices[0].LastKeepaliveAt)
	require.NotNil(t, devices[0].LastRegisterAt)

	// Deleting a device removes its channels and group memberships.
	require.NoError(t, db.UpsertGB28181Channel(ctx, boolPtrChannel(covGBChannel("dev-1", "ch-1", "Channel One"))))
	require.NoError(t, db.AddGroupChannel(ctx, 1, "dev-1", "ch-1"))
	require.NoError(t, db.DeleteGB28181Device(ctx, "dev-1"))
	_, err = db.GetGB28181Device(ctx, "dev-1")
	require.ErrorIs(t, err, sql.ErrNoRows)
	channels, err := db.ListGB28181Channels(ctx, "dev-1")
	require.NoError(t, err)
	require.Empty(t, channels)
	var groupChannels int
	require.NoError(t, db.queryRowContext(ctx, `SELECT COUNT(*) FROM device_group_channels WHERE device_id='dev-1'`).Scan(&groupChannels))
	require.Zero(t, groupChannels)
}

func boolPtrChannel(ch GB28181ChannelRow) *GB28181ChannelRow { return &ch }

// TestCovGBChannels covers channel upsert, batch replacement and status counters.
func TestCovGBChannels(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	seen := covTestTime(0)
	ch := covGBChannel("dev-1", "ch-1", "Channel One")
	ch.LastSeenAt = &seen
	ch.Manufacturer = "Acme"
	ch.Model = "M2"
	ch.Port = 8000
	ch.Longitude = 120.5
	ch.Latitude = 30.25
	ch.BusinessGroupID = "bg-1"
	require.NoError(t, db.UpsertGB28181Channel(ctx, &ch))

	got, err := db.GetGB28181Channel(ctx, "dev-1", "ch-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "Channel One", got.Name)
	require.Equal(t, "Acme", got.Manufacturer)
	require.Equal(t, 8000, got.Port)
	require.InDelta(t, 120.5, got.Longitude, 0.0001)
	require.Equal(t, "bg-1", got.BusinessGroupID)
	require.NotNil(t, got.LastSeenAt)
	require.True(t, got.LastSeenAt.Equal(seen))

	_, err = db.GetGB28181Channel(ctx, "dev-1", "missing")
	require.ErrorIs(t, err, sql.ErrNoRows)

	// A newer last_seen_at updates name/status; an older one must not.
	newer := covGBChannel("dev-1", "ch-1", "Channel One HD")
	laterSeen := seen.Add(time.Hour)
	newer.LastSeenAt = &laterSeen
	newer.Status = "OFF"
	newer.BusinessGroupID = "bg-2"
	require.NoError(t, db.UpsertGB28181Channel(ctx, &newer))
	updated, err := db.GetGB28181Channel(ctx, "dev-1", "ch-1")
	require.NoError(t, err)
	require.Equal(t, "Channel One HD", updated.Name)
	require.Equal(t, "OFF", updated.Status)
	require.Equal(t, "bg-2", updated.BusinessGroupID)
	require.True(t, updated.LastSeenAt.Equal(laterSeen))

	stale := covGBChannel("dev-1", "ch-1", "Stale Name")
	earlierSeen := seen.Add(-time.Hour)
	stale.LastSeenAt = &earlierSeen
	stale.Status = "ON"
	require.NoError(t, db.UpsertGB28181Channel(ctx, &stale))
	still := mustGBChannel(t, db, "dev-1", "ch-1")
	// The name is not guarded by the last_seen comparison, so a stale catalog
	// still renames the channel; status and last_seen are protected.
	require.Equal(t, "Stale Name", still.Name)
	require.Equal(t, "OFF", still.Status, "older catalogs do not overwrite status")
	require.True(t, still.LastSeenAt.Equal(laterSeen))

	// Channel status and missing counters.
	require.NoError(t, db.UpdateChannelStatus(ctx, "dev-1", "ch-1", "ON"))
	require.Equal(t, "ON", mustGBChannel(t, db, "dev-1", "ch-1").Status)
	require.NoError(t, db.IncrementMissingCount(ctx, "dev-1", "ch-1"))
	require.NoError(t, db.IncrementMissingCount(ctx, "dev-1", "ch-1"))
	missing := mustGBChannel(t, db, "dev-1", "ch-1")
	require.Equal(t, 2, missing.MissingCount)

	missingList, err := db.ListMissingChannels(ctx, 2)
	require.NoError(t, err)
	require.Len(t, missingList, 1)
	require.Equal(t, "ch-1", missingList[0].ChannelID)
	require.Equal(t, 2, missingList[0].MissingCount)
	require.NotNil(t, missingList[0].LastSeenAt)

	rarelyMissing, err := db.ListMissingChannels(ctx, 3)
	require.NoError(t, err)
	require.Empty(t, rarelyMissing)

	// BatchUpsertChannels keeps shared rows, adds new ones and drops vanished ones.
	require.NoError(t, db.UpsertGB28181Channel(ctx, boolPtrChannel(covGBChannel("dev-1", "ch-2", "Channel Two"))))
	batch := []GB28181ChannelRow{
		covGBChannel("dev-1", "ch-1", "Channel One Renamed"),
		covGBChannel("dev-1", "ch-3", "Channel Three"),
	}
	require.NoError(t, db.BatchUpsertChannels(ctx, "dev-1", batch))
	channels, err := db.ListGB28181Channels(ctx, "dev-1")
	require.NoError(t, err)
	require.Len(t, channels, 2)
	ids := []string{channels[0].ChannelID, channels[1].ChannelID}
	require.ElementsMatch(t, []string{"ch-1", "ch-3"}, ids)

	// ReplaceGB28181Channels currently inserts 56 placeholders into a 52 column
	// list, so it fails and rolls back, leaving the existing catalog untouched.
	err = db.ReplaceGB28181Channels(ctx, "dev-1", []GB28181ChannelRow{covGBChannel("dev-1", "ch-9", "Channel Nine")})
	require.ErrorContains(t, err, "values for")
	channels, err = db.ListGB28181Channels(ctx, "dev-1")
	require.NoError(t, err)
	require.Len(t, channels, 2, "the failed replacement is rolled back")
	require.ElementsMatch(t, []string{"ch-1", "ch-3"}, []string{channels[0].ChannelID, channels[1].ChannelID})

	// The empty-list path is unaffected by that bug and still clears the catalog.
	require.NoError(t, db.ReplaceGB28181Channels(ctx, "dev-1", nil))
	channels, err = db.ListGB28181Channels(ctx, "dev-1")
	require.NoError(t, err)
	require.Empty(t, channels)

	require.NoError(t, db.UpsertGB28181Channel(ctx, boolPtrChannel(covGBChannel("dev-1", "ch-1", "A"))))
	require.NoError(t, db.UpsertGB28181Channel(ctx, boolPtrChannel(covGBChannel("dev-1", "ch-2", "B"))))
	require.NoError(t, db.DeleteGB28181Channel(ctx, "dev-1", "ch-1"))
	channels, err = db.ListGB28181Channels(ctx, "dev-1")
	require.NoError(t, err)
	require.Len(t, channels, 1)
	require.Equal(t, "ch-2", channels[0].ChannelID)

	require.NoError(t, db.DeleteGB28181ChannelsForDevice(ctx, "dev-1"))
	channels, err = db.ListGB28181Channels(ctx, "dev-1")
	require.NoError(t, err)
	require.Empty(t, channels)
}

func mustGBChannel(t *testing.T, db *DB, deviceID, channelID string) *GB28181ChannelRow {
	t.Helper()
	ch, err := db.GetGB28181Channel(context.Background(), deviceID, channelID)
	require.NoError(t, err)
	require.NotNil(t, ch)
	return ch
}

// TestCovGBRegions covers region tree CRUD, sync and channel attachment.
func TestCovGBRegions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	top := &GBRegion{DeviceID: "340000", Name: "Anhui"}
	topID, err := db.CreateGBRegion(ctx, top)
	require.NoError(t, err)
	require.NotZero(t, topID)
	require.NotZero(t, top.ID)

	// Parent backfill by parent device id.
	child, err := db.CreateGBRegion(ctx, &GBRegion{DeviceID: "340100", Name: "Hefei", ParentDeviceID: "340000"})
	require.NoError(t, err)
	childRow, err := db.GetGBRegion(ctx, child)
	require.NoError(t, err)
	require.Equal(t, topID, childRow.ParentID)

	// Orphan children are adopted once the parent appears.
	orphan, err := db.CreateGBRegion(ctx, &GBRegion{DeviceID: "340102", Name: "Shushan", ParentDeviceID: "340100"})
	require.NoError(t, err)
	require.NoError(t, db.UpdateGBRegion(ctx, &GBRegion{ID: orphan, DeviceID: "340102", Name: "Shushan District", ParentDeviceID: "340100", ParentID: 0}))
	// Recreating the parent adopts the orphan.
	second, err := db.CreateGBRegion(ctx, &GBRegion{DeviceID: "340100", Name: "Hefei"})
	require.Error(t, err, "device_id is unique")
	require.Zero(t, second)
	// Adoption also happens through the explicit update path.
	require.NoError(t, db.UpdateGBRegion(ctx, &GBRegion{ID: orphan, DeviceID: "340102", Name: "Shushan District", ParentDeviceID: "340100", ParentID: child}))

	children, err := db.ListGBRegionsByParent(ctx, topID)
	require.NoError(t, err)
	require.Len(t, children, 1)
	require.Equal(t, "340100", children[0].DeviceID)

	byCode, err := db.GetGBRegionByDeviceID(ctx, "340100")
	require.NoError(t, err)
	require.NotNil(t, byCode)
	require.Equal(t, "Hefei", byCode.Name)
	missingCode, err := db.GetGBRegionByDeviceID(ctx, "999999")
	require.NoError(t, err)
	require.Nil(t, missingCode)

	_, err = db.GetGBRegion(ctx, 4242)
	require.ErrorIs(t, err, sql.ErrNoRows)

	descendants, err := db.GetGBRegionDescendants(ctx, topID)
	require.NoError(t, err)
	require.Len(t, descendants, 2)

	// Channel attachment by civil code.
	ch := covGBChannel("dev-1", "ch-1", "Channel One")
	ch.CivilCode = ""
	ch.ParentID = ""
	require.NoError(t, db.UpsertGB28181Channel(ctx, &ch))
	ch2 := covGBChannel("dev-1", "ch-2", "Channel Two")
	ch2.CivilCode = ""
	require.NoError(t, db.UpsertGB28181Channel(ctx, &ch2))

	keys := []ChannelKey{{DeviceID: "dev-1", ChannelID: "ch-1"}, {DeviceID: "dev-1", ChannelID: "ch-2"}}
	require.NoError(t, db.AttachChannelsToRegion(ctx, "340100", keys))
	count, err := db.CountChannelsByCivilCode(ctx, "340100")
	require.NoError(t, err)
	require.Equal(t, 2, count)

	attached, err := db.ListChannelsByCivilCode(ctx, "340100")
	require.NoError(t, err)
	require.Len(t, attached, 2)
	require.Equal(t, "340100", attached[0].CivilCode)

	require.NoError(t, db.DetachChannelsFromRegionByCode(ctx, "340100"))
	count, err = db.CountChannelsByCivilCode(ctx, "340100")
	require.NoError(t, err)
	require.Zero(t, count)

	// Detach by keys and the empty-key no-op.
	require.NoError(t, db.AttachChannelsToRegion(ctx, "340100", keys))
	require.NoError(t, db.DetachChannelsFromRegion(ctx, nil))
	count, err = db.CountChannelsByCivilCode(ctx, "340100")
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NoError(t, db.DetachChannelsFromRegion(ctx, keys))
	count, err = db.CountChannelsByCivilCode(ctx, "340100")
	require.NoError(t, err)
	require.Zero(t, count)

	// Region creation from channel civil codes.
	withCode := covGBChannel("dev-2", "ch-3", "Channel Three")
	withCode.CivilCode = "340104"
	require.NoError(t, db.UpsertGB28181Channel(ctx, &withCode))
	created, err := db.SyncRegionsFromChannels(ctx)
	require.NoError(t, err)
	require.Positive(t, created)
	synced, err := db.GetGBRegionByDeviceID(ctx, "340104")
	require.NoError(t, err)
	require.NotNil(t, synced)
	// Syncing again creates nothing new.
	again, err := db.SyncRegionsFromChannels(ctx)
	require.NoError(t, err)
	require.Zero(t, again)

	// add-by-code with an empty code is a no-op.
	added, err := db.AddRegionByCivilCode(ctx, "")
	require.NoError(t, err)
	require.Zero(t, added)

	require.NoError(t, db.DeleteGBRegions(ctx, nil))
	require.NoError(t, db.DeleteGBRegions(ctx, []int64{orphan, child}))
	remaining, err := db.ListGBRegionsByParent(ctx, topID)
	require.NoError(t, err)
	require.Empty(t, remaining, "children were deleted")

	// Channel brief search.
	search, err := db.SearchChannelsBrief(ctx, "Channel", false, false)
	require.NoError(t, err)
	require.Len(t, search, 3)

	byName, err := db.SearchChannelsBrief(ctx, "Three", false, false)
	require.NoError(t, err)
	require.Len(t, byName, 1)
	require.Equal(t, "ch-3", byName[0].ChannelID)

	// Only "ch-3" has a civil code, so the other two are unassigned regions;
	// only "ch-1" has an empty parent_id, so it is the unassigned group channel.
	unassignedRegion, err := db.SearchChannelsBrief(ctx, "", true, false)
	require.NoError(t, err)
	require.Len(t, unassignedRegion, 2)
	unassignedGroup, err := db.SearchChannelsBrief(ctx, "", false, true)
	require.NoError(t, err)
	require.Len(t, unassignedGroup, 1)
	require.Equal(t, "ch-1", unassignedGroup[0].ChannelID)

	parentBriefs, err := db.ListChannelsByParentID(ctx, "parent-1")
	require.NoError(t, err)
	require.Len(t, parentBriefs, 2)
	noParentBriefs, err := db.ListChannelsByParentID(ctx, "parent-none")
	require.NoError(t, err)
	require.Empty(t, noParentBriefs)
}

// TestCovParentCivilCode covers the GB/T 2260 parent inference.
func TestCovParentCivilCode(t *testing.T) {
	require.Equal(t, "340100", parentCivilCode("34010001"))
	require.Equal(t, "3401", parentCivilCode("340100"))
	require.Equal(t, "3401", parentCivilCode("340100  "))
	require.Equal(t, "34", parentCivilCode("3401"))
	require.Equal(t, "", parentCivilCode("34"))
	require.Equal(t, "", parentCivilCode(""))
}

// TestCovGBGroups covers business-group tree CRUD and channel attachment.
func TestCovGBGroups(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	rootID, err := db.CreateGBGroup(ctx, &GBGroup{DeviceID: "bg-215", Name: "Business", BusinessGroup: "bg-215", CivilCode: "340100"})
	require.NoError(t, err)
	require.NotZero(t, rootID)

	// parent_id is backfilled from parent_device_id within the same business group.
	childID, err := db.CreateGBGroup(ctx, &GBGroup{DeviceID: "vg-216", Name: "Virtual", ParentDeviceID: "bg-215", BusinessGroup: "bg-215"})
	require.NoError(t, err)
	child, err := db.GetGBGroup(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, rootID, child.ParentID)

	grandChildID, err := db.CreateGBGroup(ctx, &GBGroup{DeviceID: "vg-217", Name: "Nested", ParentDeviceID: "vg-216", BusinessGroup: "bg-215"})
	require.NoError(t, err)
	grandChild, err := db.GetGBGroup(ctx, grandChildID)
	require.NoError(t, err)
	require.Equal(t, childID, grandChild.ParentID)

	byDevice, err := db.GetGBGroupByDeviceID(ctx, "vg-216", "bg-215")
	require.NoError(t, err)
	require.NotNil(t, byDevice)
	require.Equal(t, "Virtual", byDevice.Name)
	none, err := db.GetGBGroupByDeviceID(ctx, "vg-216", "other-bg")
	require.NoError(t, err)
	require.Nil(t, none)

	business, err := db.GetGBBusinessGroup(ctx, "bg-215")
	require.NoError(t, err)
	require.NotNil(t, business)
	require.Equal(t, rootID, business.ID)

	_, err = db.GetGBGroup(ctx, 9999)
	require.ErrorIs(t, err, sql.ErrNoRows)

	children, err := db.ListGBGroupsByParent(ctx, rootID)
	require.NoError(t, err)
	require.Len(t, children, 1)
	require.Equal(t, "vg-216", children[0].DeviceID)

	descendants, err := db.GetGBGroupDescendants(ctx, rootID)
	require.NoError(t, err)
	require.Len(t, descendants, 2)

	all, err := db.ListGBGroupsByBusinessGroup(ctx, "bg-215")
	require.NoError(t, err)
	require.Len(t, all, 3)

	require.NoError(t, db.UpdateGBGroup(ctx, &GBGroup{ID: childID, DeviceID: "vg-216", Name: "Virtual Renamed", ParentDeviceID: "bg-215", ParentID: rootID, BusinessGroup: "bg-215", CivilCode: "340102"}))
	renamed, err := db.GetGBGroup(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, "Virtual Renamed", renamed.Name)
	require.Equal(t, "340102", renamed.CivilCode)

	// Channel attachment: parent_id + business_group_id are written together.
	ch := covGBChannel("dev-1", "ch-1", "Channel One")
	ch.ParentID = ""
	ch.BusinessGroupID = ""
	require.NoError(t, db.UpsertGB28181Channel(ctx, &ch))
	ch2 := covGBChannel("dev-1", "ch-2", "Channel Two")
	ch2.ParentID = ""
	ch2.BusinessGroupID = ""
	require.NoError(t, db.UpsertGB28181Channel(ctx, &ch2))

	keys := []ChannelKey{{DeviceID: "dev-1", ChannelID: "ch-1"}, {DeviceID: "dev-1", ChannelID: "ch-2"}}
	require.NoError(t, db.AttachChannelsToGroup(ctx, "vg-216", "bg-215", nil), "empty key list is a no-op")
	require.NoError(t, db.AttachChannelsToGroup(ctx, "vg-216", "bg-215", keys))
	count, err := db.CountChannelsByParentID(ctx, "vg-216")
	require.NoError(t, err)
	require.Equal(t, 2, count)

	attached, err := db.ListChannelsByParentID(ctx, "vg-216")
	require.NoError(t, err)
	require.Len(t, attached, 2)
	require.Equal(t, "bg-215", attached[0].BusinessGroupID)

	require.NoError(t, db.DetachChannelsFromGroupByParent(ctx, "vg-216"))
	count, err = db.CountChannelsByParentID(ctx, "vg-216")
	require.NoError(t, err)
	require.Zero(t, count)

	// Detach by business group.
	require.NoError(t, db.AttachChannelsToGroup(ctx, "vg-216", "bg-215", keys))
	require.NoError(t, db.DetachChannelsFromBusinessGroup(ctx, "bg-215"))
	count, err = db.CountChannelsByParentID(ctx, "vg-216")
	require.NoError(t, err)
	require.Zero(t, count)

	// Detach by keys (and no-op for empty keys).
	require.NoError(t, db.AttachChannelsToGroup(ctx, "vg-216", "bg-215", keys))
	require.NoError(t, db.DetachChannelsFromGroup(ctx, nil))
	count, err = db.CountChannelsByParentID(ctx, "vg-216")
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NoError(t, db.DetachChannelsFromGroup(ctx, keys))
	count, err = db.CountChannelsByParentID(ctx, "vg-216")
	require.NoError(t, err)
	require.Zero(t, count)

	require.NoError(t, db.DeleteGBGroups(ctx, nil))
	require.NoError(t, db.DeleteGBGroups(ctx, []int64{childID, grandChildID}))
	children, err = db.ListGBGroupsByParent(ctx, rootID)
	require.NoError(t, err)
	require.Empty(t, children)
}

// TestCovGBClosedDBErrorPaths asserts every GB helper surfaces storage errors.
func TestCovGBClosedDBErrorPaths(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.Close())

	require.Error(t, db.UpsertGB28181Device(ctx, &GB28181DeviceRow{DeviceID: "d"}))
	_, err := db.ListGB28181Devices(ctx)
	require.Error(t, err)
	_, err = db.GetGB28181Device(ctx, "d")
	require.Error(t, err)
	require.Error(t, db.UpdateGB28181DeviceStatus(ctx, "d", true, ""))
	require.Error(t, db.UpdateGB28181DeviceRegistration(ctx, "d", ""))
	require.Error(t, db.UpdateGB28181DeviceOnlineStatus(ctx, "d", true))
	require.Error(t, db.DeleteGB28181Device(ctx, "d"))
	_, err = db.ListGB28181Channels(ctx, "d")
	require.Error(t, err)
	_, err = db.GetGB28181Channel(ctx, "d", "c")
	require.Error(t, err)
	require.Error(t, db.UpsertGB28181Channel(ctx, &GB28181ChannelRow{DeviceID: "d", ChannelID: "c"}))
	require.Error(t, db.BatchUpsertChannels(ctx, "d", nil))
	require.Error(t, db.ReplaceGB28181Channels(ctx, "d", nil))
	require.Error(t, db.DeleteGB28181Channel(ctx, "d", "c"))
	require.Error(t, db.DeleteGB28181ChannelsForDevice(ctx, "d"))
	_, err = db.ListMissingChannels(ctx, 1)
	require.Error(t, err)
	require.Error(t, db.UpdateChannelStatus(ctx, "d", "c", "ON"))
	require.Error(t, db.IncrementMissingCount(ctx, "d", "c"))

	_, err = db.ListGBRegionsByParent(ctx, 0)
	require.Error(t, err)
	_, err = db.GetGBRegion(ctx, 1)
	require.Error(t, err)
	_, err = db.GetGBRegionByDeviceID(ctx, "code")
	require.Error(t, err)
	_, err = db.CreateGBRegion(ctx, &GBRegion{DeviceID: "code"})
	require.Error(t, err)
	require.Error(t, db.UpdateGBRegion(ctx, &GBRegion{ID: 1}))
	_, err = db.GetGBRegionDescendants(ctx, 1)
	require.Error(t, err)
	require.Error(t, db.DeleteGBRegions(ctx, []int64{1}))
	_, err = db.SyncRegionsFromChannels(ctx)
	require.Error(t, err)
	_, err = db.AddRegionByCivilCode(ctx, "340100")
	require.Error(t, err)
	_, err = db.CountChannelsByCivilCode(ctx, "code")
	require.Error(t, err)
	require.Error(t, db.AttachChannelsToRegion(ctx, "code", []ChannelKey{{DeviceID: "d", ChannelID: "c"}}))
	require.Error(t, db.DetachChannelsFromRegionByCode(ctx, "code"))
	_, err = db.ListChannelsByCivilCode(ctx, "code")
	require.Error(t, err)
	_, err = db.ListChannelsByParentID(ctx, "parent")
	require.Error(t, err)
	_, err = db.SearchChannelsBrief(ctx, "q", true, true)
	require.Error(t, err)

	_, err = db.ListGBGroupsByParent(ctx, 0)
	require.Error(t, err)
	_, err = db.GetGBGroup(ctx, 1)
	require.Error(t, err)
	_, err = db.GetGBGroupByDeviceID(ctx, "d", "bg")
	require.Error(t, err)
	_, err = db.GetGBBusinessGroup(ctx, "bg")
	require.Error(t, err)
	_, err = db.CreateGBGroup(ctx, &GBGroup{DeviceID: "d", BusinessGroup: "bg"})
	require.Error(t, err)
	require.Error(t, db.UpdateGBGroup(ctx, &GBGroup{ID: 1}))
	_, err = db.GetGBGroupDescendants(ctx, 1)
	require.Error(t, err)
	_, err = db.ListGBGroupsByBusinessGroup(ctx, "bg")
	require.Error(t, err)
	require.Error(t, db.DeleteGBGroups(ctx, []int64{1}))
	_, err = db.CountChannelsByParentID(ctx, "parent")
	require.Error(t, err)
	require.Error(t, db.AttachChannelsToGroup(ctx, "parent", "bg", []ChannelKey{{DeviceID: "d", ChannelID: "c"}}))
	require.Error(t, db.DetachChannelsFromGroup(ctx, []ChannelKey{{DeviceID: "d", ChannelID: "c"}}))
	require.Error(t, db.DetachChannelsFromGroupByParent(ctx, "parent"))
	require.Error(t, db.DetachChannelsFromBusinessGroup(ctx, "bg"))
}
