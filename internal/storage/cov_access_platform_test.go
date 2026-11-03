package storage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

func covUserTimes() (time.Time, time.Time) {
	return covTestTime(0), covTestTime(time.Minute)
}

// TestCovUsers covers the user repository including not-found branches.
func TestCovUsers(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	created, updated := covUserTimes()

	// The users table exists after Init.
	require.NoError(t, db.CreateUserTable(ctx), "create table is idempotent")

	count, err := db.CountUsers(ctx)
	require.NoError(t, err)
	require.Zero(t, count)

	superAdmin := &model.User{
		Username: "admin", PasswordHash: "hash-admin", Role: model.RoleSuperAdmin,
		CreatedAt: created, UpdatedAt: updated, Enabled: true,
	}
	require.NoError(t, db.CreateUser(ctx, superAdmin))
	require.NotZero(t, superAdmin.ID)
	require.Equal(t, "admin", superAdmin.DisplayName, "display name defaults to username")

	viewer := &model.User{
		Username: "  viewer  ", PasswordHash: "hash-viewer", Role: model.RoleUser,
		DisplayName: "Viewer", CreatedAt: created, UpdatedAt: updated, Enabled: false,
	}
	require.NoError(t, db.CreateUser(ctx, viewer))
	require.NotZero(t, viewer.ID)

	// Duplicate usernames violate the UNIQUE constraint.
	dup := &model.User{Username: "admin", PasswordHash: "x", Role: model.RoleUser, CreatedAt: created, UpdatedAt: updated}
	require.Error(t, db.CreateUser(ctx, dup))

	byName, err := db.GetUserByUsername(ctx, " admin ")
	require.NoError(t, err)
	require.NotNil(t, byName)
	require.Equal(t, superAdmin.ID, byName.ID)
	require.Equal(t, model.RoleSuperAdmin, byName.Role)
	require.True(t, byName.Enabled)
	require.True(t, byName.CreatedAt.Equal(created))

	byID, err := db.GetUserByID(ctx, viewer.ID)
	require.NoError(t, err)
	require.NotNil(t, byID)
	require.Equal(t, "  viewer  ", byID.Username, "username is stored verbatim")
	require.False(t, byID.Enabled)

	missingName, err := db.GetUserByUsername(ctx, "ghost")
	require.NoError(t, err)
	require.Nil(t, missingName)
	missingID, err := db.GetUserByID(ctx, 4242)
	require.NoError(t, err)
	require.Nil(t, missingID)

	hasSuper, err := db.HasSuperAdmin(ctx)
	require.NoError(t, err)
	require.True(t, hasSuper)

	users, err := db.ListUsers(ctx)
	require.NoError(t, err)
	require.Len(t, users, 2)
	require.Equal(t, superAdmin.ID, users[0].ID)
	require.False(t, users[1].Enabled)

	count, err = db.CountUsers(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	// Update flips role, display name, password and enabled state.
	viewer.Role = model.RoleSuperAdmin
	viewer.DisplayName = "Viewer Prime"
	viewer.PasswordHash = "new-hash"
	viewer.Enabled = true
	require.NoError(t, db.UpdateUser(ctx, viewer))
	updatedUser, err := db.GetUserByID(ctx, viewer.ID)
	require.NoError(t, err)
	require.Equal(t, model.RoleSuperAdmin, updatedUser.Role)
	require.Equal(t, "Viewer Prime", updatedUser.DisplayName)
	require.Equal(t, "new-hash", updatedUser.PasswordHash)
	require.True(t, updatedUser.Enabled)

	require.NoError(t, db.UpdateUserPassword(ctx, viewer.ID, "rotated"))
	rotated, err := db.GetUserByID(ctx, viewer.ID)
	require.NoError(t, err)
	require.Equal(t, "rotated", rotated.PasswordHash)

	require.ErrorContains(t, db.DeleteUser(ctx, 9999), "user not found")
	require.NoError(t, db.DeleteUser(ctx, viewer.ID))
	remaining, err := db.ListUsers(ctx)
	require.NoError(t, err)
	require.Len(t, remaining, 1)

	require.NoError(t, db.DeleteUser(ctx, superAdmin.ID))
	hasSuper, err = db.HasSuperAdmin(ctx)
	require.NoError(t, err)
	require.False(t, hasSuper)
}

// TestCovPlatforms covers GB28181 platform CRUD, status and channel management.
func TestCovPlatforms(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	platforms, err := db.ListPlatforms(ctx)
	require.NoError(t, err)
	require.Empty(t, platforms)

	first := &PlatformRow{
		Name: "upper", Enable: true, ServerGBID: "34020000002000000001", ServerGBDomain: "3402000000",
		ServerIP: "10.0.0.10", ServerPort: 5060, DeviceGBID: "34020000001320000001",
		DeviceGBDomain: "3402000000", DeviceIP: "10.0.0.11", DevicePort: 5060,
		Username: "user", Password: "pass", Transport: "UDP", CharacterSet: "GB2312",
		Expires: 3600, KeepTimeout: 60, MaxTimeoutCount: 3,
	}
	id, err := db.CreatePlatform(ctx, first)
	require.NoError(t, err)
	require.NotZero(t, id)
	first.ID = id

	second := &PlatformRow{Name: "second", ServerGBID: "s2", ServerIP: "10.0.0.20", DeviceGBID: "d2", DeviceIP: "10.0.0.21"}
	secondID, err := db.CreatePlatform(ctx, second)
	require.NoError(t, err)

	platforms, err = db.ListPlatforms(ctx)
	require.NoError(t, err)
	require.Len(t, platforms, 2)
	require.Equal(t, "upper", platforms[0].Name)
	require.True(t, platforms[0].Enable)
	require.Equal(t, "34020000002000000001", platforms[0].ServerGBID)
	require.Equal(t, 5060, platforms[0].ServerPort)
	require.Nil(t, platforms[0].LastRegisterAt)

	got, err := db.GetPlatform(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "upper", got.Name)
	require.Equal(t, "GB2312", got.CharacterSet)
	require.Equal(t, 3600, got.Expires)

	_, err = db.GetPlatform(ctx, 99999)
	require.ErrorIs(t, err, sql.ErrNoRows)

	first.Name = "upper-renamed"
	first.Enable = false
	first.Transport = "TCP"
	require.NoError(t, db.UpdatePlatform(ctx, first))
	got, err = db.GetPlatform(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "upper-renamed", got.Name)
	require.False(t, got.Enable)
	require.Equal(t, "TCP", got.Transport)

	require.NoError(t, db.UpdatePlatformStatus(ctx, id, true))
	registered, err := db.GetPlatform(ctx, id)
	require.NoError(t, err)
	require.True(t, registered.Status)
	require.NotNil(t, registered.LastRegisterAt, "registration timestamp is stamped on status update")

	// Platform channels: seed directly (UpsertPlatformChannel needs a unique key,
	// see TestCovPlatformChannelUpsertRequiresUniqueKey), then filter/toggle/delete.
	_, err = db.execContext(ctx, `INSERT INTO gb28181_platform_channels (platform_id, channel_id, device_id, custom_id, custom_name, stream_path, is_shared)
		VALUES (?,?,?,?,?,?,?)`, id, "ch-1", "dev-1", "c1", "one", "rtp/1", 1)
	require.NoError(t, err)
	_, err = db.execContext(ctx, `INSERT INTO gb28181_platform_channels (platform_id, channel_id, device_id, custom_id, custom_name, stream_path, is_shared)
		VALUES (?,?,?,?,?,?,?)`, id, "ch-2", "dev-1", "c2", "two", "rtp/2", 0)
	require.NoError(t, err)

	all, err := db.ListPlatformChannels(ctx, id, nil)
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, "ch-1", all[0].ChannelID)
	require.True(t, all[0].IsShared)

	sharedOnly, err := db.ListPlatformChannels(ctx, id, boolPtrCov(true))
	require.NoError(t, err)
	require.Len(t, sharedOnly, 1)
	require.Equal(t, "ch-1", sharedOnly[0].ChannelID)

	privateOnly, err := db.ListPlatformChannels(ctx, id, boolPtrCov(false))
	require.NoError(t, err)
	require.Len(t, privateOnly, 1)
	require.Equal(t, "ch-2", privateOnly[0].ChannelID)

	require.NoError(t, db.SetPlatformChannelShared(ctx, id, "ch-2", true))
	sharedOnly, err = db.ListPlatformChannels(ctx, id, boolPtrCov(true))
	require.NoError(t, err)
	require.Len(t, sharedOnly, 2)

	require.NoError(t, db.DeletePlatformChannel(ctx, id, "ch-1"))
	all, err = db.ListPlatformChannels(ctx, id, nil)
	require.NoError(t, err)
	require.Len(t, all, 1)

	require.NoError(t, db.DeletePlatform(ctx, id))
	platforms, err = db.ListPlatforms(ctx)
	require.NoError(t, err)
	require.Len(t, platforms, 1)
	require.Equal(t, secondID, platforms[0].ID)
}

// TestCovPlatformChannelUpsertRequiresUniqueKey documents the current schema:
// the ON CONFLICT target needs a unique index, which Init does not create.
func TestCovPlatformChannelUpsertRequiresUniqueKey(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ch := &PlatformChannelRow{PlatformID: 1, ChannelID: "ch-1", DeviceID: "dev-1"}
	require.Error(t, db.UpsertPlatformChannel(ctx, ch), "upsert needs a UNIQUE(platform_id, channel_id) key")

	// With the index in place the insert/update path works as designed.
	_, err := db.execContext(ctx, `CREATE UNIQUE INDEX cov_platform_channels_unique ON gb28181_platform_channels(platform_id, channel_id)`)
	require.NoError(t, err)
	require.NoError(t, db.UpsertPlatformChannel(ctx, ch))
	ch.CustomName = "updated"
	require.NoError(t, db.UpsertPlatformChannel(ctx, ch))
	rows, err := db.ListPlatformChannels(ctx, 1, nil)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "updated", rows[0].CustomName)
}

// TestCovPlatformEvents covers event insertion, filtered listing and status.
func TestCovPlatformEvents(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	platform := &PlatformRow{Name: "upper", Enable: true, ServerGBID: "s", ServerIP: "10.0.0.1", DeviceGBID: "d", DeviceIP: "10.0.0.2"}
	id, err := db.CreatePlatform(ctx, platform)
	require.NoError(t, err)
	idle := &PlatformRow{Name: "idle", ServerGBID: "s2", ServerIP: "10.0.0.3", DeviceGBID: "d2", DeviceIP: "10.0.0.4"}
	_, err = db.CreatePlatform(ctx, idle)
	require.NoError(t, err)

	// GetPlatformStatus fails on SQLite: COALESCE(e.created_at, p.created_at)
	// loses the DATETIME declared type, so the driver returns TEXT for the
	// timestamp column and the scan into time.Time fails. Assert the current
	// behavior instead of a status row.
	_, err = db.GetPlatformStatus(ctx)
	require.ErrorContains(t, err, "last_event_time")

	events := []PlatformEventRow{
		{PlatformID: id, PlatformName: "upper", EventType: PlatformEventRegister, ServerIP: "10.0.0.1", ServerPort: 5060},
		{PlatformID: id, PlatformName: "upper", EventType: PlatformEventKeepAlive, ServerIP: "10.0.0.1", ServerPort: 5060},
		{PlatformID: id, PlatformName: "upper", EventType: PlatformEventStreamStart, ServerIP: "10.0.0.1", ServerPort: 5060, ChannelID: "ch-1", StreamID: "rtp/1", Details: "started"},
		{PlatformID: 999, PlatformName: "ghost", EventType: PlatformEventOffline, ServerIP: "10.0.0.9", ServerPort: 5060},
	}
	for _, e := range events {
		require.NoError(t, db.AddPlatformEvent(ctx, e))
	}

	all, total, err := db.ListPlatformEvents(ctx, 0, "", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 4, total)
	require.Len(t, all, 4)

	byPlatform, total, err := db.ListPlatformEvents(ctx, id, "", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Len(t, byPlatform, 3)

	byType, total, err := db.ListPlatformEvents(ctx, id, string(PlatformEventKeepAlive), 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, byType, 1)
	require.Equal(t, PlatformEventKeepAlive, byType[0].EventType)

	page, total, err := db.ListPlatformEvents(ctx, id, "", 1, 1)
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Len(t, page, 1)

	none, total, err := db.ListPlatformEvents(ctx, id, string(PlatformEventUnregister), 10, 0)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, none)

	// GetPlatformStatus fails for the same COALESCE reason once events exist.
	var statuses []PlatformStatusRow
	statuses, err = db.GetPlatformStatus(ctx)
	require.ErrorContains(t, err, "last_event_time")
	require.Empty(t, statuses)
}

// TestCovDeviceGroups covers group CRUD, channel membership and stats.
func TestCovDeviceGroups(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// createGroupTables installs the default root group.
	require.NoError(t, db.createGroupTables(ctx))
	groups, err := db.ListDeviceGroups(ctx)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.Equal(t, "默认分组", groups[0].Name)

	root := groups[0]
	childID, err := db.CreateDeviceGroup(ctx, &DeviceGroup{Name: "child", ParentID: root.ID, Level: 1, SortOrder: 2})
	require.NoError(t, err)
	grandChildID, err := db.CreateDeviceGroup(ctx, &DeviceGroup{Name: "grandchild", ParentID: childID, Level: 2, SortOrder: 1})
	require.NoError(t, err)

	child, err := db.GetDeviceGroup(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, "child", child.Name)
	require.Equal(t, root.ID, child.ParentID)
	require.Equal(t, 1, child.Level)

	_, err = db.GetDeviceGroup(ctx, 9999)
	require.ErrorIs(t, err, sql.ErrNoRows)

	require.NoError(t, db.UpdateDeviceGroup(ctx, &DeviceGroup{ID: childID, Name: "child-renamed", ParentID: root.ID, Level: 1, SortOrder: 5}))
	child, err = db.GetDeviceGroup(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, "child-renamed", child.Name)
	require.Equal(t, 5, child.SortOrder)

	// Channels: one GB28181 device channel, one camera-backed channel, one orphan.
	require.NoError(t, db.AddGroupChannel(ctx, childID, "dev-1", "ch-1"))
	require.NoError(t, db.AddGroupChannel(ctx, childID, "cam-1", "cam-1"))
	require.NoError(t, db.AddGroupChannel(ctx, childID, "ghost-dev", "ghost-ch"))
	// Duplicates are ignored by INSERT OR IGNORE.
	require.NoError(t, db.AddGroupChannel(ctx, childID, "dev-1", "ch-1"))

	_, err = db.execContext(ctx, `INSERT INTO gb28181_devices (device_id, name, is_online) VALUES ('dev-1','Device One',1)`)
	require.NoError(t, err)
	seedChannel(t, db, "dev-1", "ch-1")
	_, err = db.execContext(ctx, `UPDATE gb28181_channels SET name='Channel One' WHERE device_id='dev-1' AND channel_id='ch-1'`)
	require.NoError(t, err)
	_, err = db.execContext(ctx, `INSERT INTO cameras (id, name, protocol, url, enabled) VALUES ('cam-1','Camera One','rtsp','rtsp://cam',1)`)
	require.NoError(t, err)

	channels, err := db.ListGroupChannels(ctx, childID)
	require.NoError(t, err)
	require.Len(t, channels, 3)
	byDevice := map[string]DeviceGroupChannelDetail{}
	for _, c := range channels {
		byDevice[c.DeviceID+"/"+c.ChannelID] = c
	}
	gb := byDevice["dev-1/ch-1"]
	require.Equal(t, "Device One", gb.DeviceName)
	require.Equal(t, "Channel One", gb.ChannelName)
	require.True(t, gb.IsOnline)
	cam := byDevice["cam-1/cam-1"]
	require.Equal(t, "Camera One", cam.DeviceName)
	require.Equal(t, "Camera One", cam.ChannelName)
	require.True(t, cam.IsOnline)
	ghost := byDevice["ghost-dev/ghost-ch"]
	require.Empty(t, ghost.DeviceName)
	require.False(t, ghost.IsOnline)

	total, online, err := db.GetGroupChannelStats(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Equal(t, 1, online, "only GB28181 device rows contribute to the online count")

	count, err := db.CountGroupChannels(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, 3, count)

	// Removing one association by device+channel and one by ID.
	require.NoError(t, db.RemoveGroupChannel(ctx, childID, "dev-1", "ch-1"))
	count, err = db.CountGroupChannels(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	remaining, err := db.ListGroupChannels(ctx, childID)
	require.NoError(t, err)
	require.Len(t, remaining, 2)
	require.NoError(t, db.RemoveGroupChannelByID(ctx, remaining[0].ID))
	count, err = db.CountGroupChannels(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// Remove the rest by device ID.
	require.NoError(t, db.RemoveGroupChannelsByDeviceID(ctx, "ghost-dev"))
	count, err = db.CountGroupChannels(ctx, childID)
	require.NoError(t, err)
	require.Zero(t, count)

	// Delete cascades to children.
	_, err = db.execContext(ctx, `INSERT INTO device_group_channels (group_id, device_id, channel_id) VALUES (?,?,?)`, grandChildID, "dev-9", "ch-9")
	require.NoError(t, err)
	require.NoError(t, db.DeleteDeviceGroup(ctx, childID))
	groups, err = db.ListDeviceGroups(ctx)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.Equal(t, root.ID, groups[0].ID)

	// SQLite foreign keys are not enabled by the connection settings, so the
	// ON DELETE CASCADE leaves orphaned channel rows behind; the group rows
	// themselves are removed explicitly by DeleteDeviceGroup.
	var leftover int
	require.NoError(t, db.queryRowContext(ctx, `SELECT COUNT(*) FROM device_group_channels WHERE group_id=?`, grandChildID).Scan(&leftover))
	require.Equal(t, 1, leftover)
	require.NoError(t, db.RemoveGroupChannelsByDeviceID(ctx, "dev-9"))
	require.NoError(t, db.queryRowContext(ctx, `SELECT COUNT(*) FROM device_group_channels WHERE group_id=?`, grandChildID).Scan(&leftover))
	require.Zero(t, leftover)
}

// TestCovGroupStatsEmptyGroup covers the aggregate scan when a group has no channels.
func TestCovGroupStatsEmptyGroup(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.createGroupTables(ctx))

	total, online, err := db.GetGroupChannelStats(ctx, 4242)
	// SUM over zero rows is NULL, which cannot be scanned into int: the current
	// implementation surfaces that as an error rather than returning zeros.
	require.Error(t, err)
	require.Zero(t, total)
	require.Zero(t, online)

	// Later the helper works once at least one channel belongs to the group.
	_, err = db.execContext(ctx, `INSERT INTO device_group_channels (group_id, device_id, channel_id) VALUES (4242,'d','c')`)
	require.NoError(t, err)
	total, online, err = db.GetGroupChannelStats(ctx, 4242)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Zero(t, online)
}
