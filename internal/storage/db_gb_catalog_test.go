package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func newCatalogTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Init(context.Background()); err != nil {
		t.Fatalf("init db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedChannel(t *testing.T, db *DB, deviceID, channelID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx,
		"INSERT INTO gb28181_channels (device_id, channel_id, name) VALUES (?, ?, ?);",
		deviceID, channelID, channelID); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
}

func TestGBRegionCRUDAndAttach(t *testing.T) {
	ctx := context.Background()
	db := newCatalogTestDB(t)

	// 建省 -> 市两级
	prov := &GBRegion{DeviceID: "4401", Name: "广东省"}
	if _, err := db.CreateGBRegion(ctx, prov); err != nil {
		t.Fatalf("create province: %v", err)
	}
	city := &GBRegion{DeviceID: "440100", Name: "广州市", ParentDeviceID: "4401"}
	if _, err := db.CreateGBRegion(ctx, city); err != nil {
		t.Fatalf("create city: %v", err)
	}
	if city.ParentID != prov.ID {
		t.Fatalf("city parent_id = %d, want %d", city.ParentID, prov.ID)
	}

	// 顶层应只有省
	top, err := db.ListGBRegionsByParent(ctx, 0)
	if err != nil || len(top) != 1 || top[0].DeviceID != "4401" {
		t.Fatalf("top regions = %+v, err=%v", top, err)
	}

	// 挂接通道到市
	seedChannel(t, db, "34020000001320000001", "34020000001320000001")
	key := ChannelKey{DeviceID: "34020000001320000001", ChannelID: "34020000001320000001"}
	if err := db.AttachChannelsToRegion(ctx, "440100", []ChannelKey{key}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	chs, err := db.ListChannelsByCivilCode(ctx, "440100")
	if err != nil || len(chs) != 1 {
		t.Fatalf("list by civil code = %+v, err=%v", chs, err)
	}

	// 删除省 -> 级联删市, 通道置空
	desc, err := db.GetGBRegionDescendants(ctx, prov.ID)
	if err != nil || len(desc) != 1 {
		t.Fatalf("descendants = %+v, err=%v", desc, err)
	}
	_ = db.DetachChannelsFromRegionByCode(ctx, "440100")
	if err := db.DeleteGBRegions(ctx, []int64{prov.ID, city.ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n, _ := db.CountChannelsByCivilCode(ctx, "440100"); n != 0 {
		t.Fatalf("channels still attached: %d", n)
	}
}

func TestAddRegionByCivilCode(t *testing.T) {
	ctx := context.Background()
	db := newCatalogTestDB(t)

	// 440106 天河区 -> 4401 广州市 -> 44 广东省
	created, err := db.AddRegionByCivilCode(ctx, "440106")
	if err != nil {
		t.Fatalf("add by civil code: %v", err)
	}
	if created != 3 {
		t.Fatalf("expected 3 nodes created, got %d", created)
	}

	prov, _ := db.GetGBRegionByDeviceID(ctx, "44")
	city, _ := db.GetGBRegionByDeviceID(ctx, "4401")
	dist, _ := db.GetGBRegionByDeviceID(ctx, "440106")
	if prov == nil || city == nil || dist == nil {
		t.Fatalf("chain incomplete: prov=%v city=%v dist=%v", prov, city, dist)
	}
	// 名称来自标准库
	if prov.Name != "广东省" || city.Name != "广州市" || dist.Name != "天河区" {
		t.Fatalf("names wrong: %q/%q/%q", prov.Name, city.Name, dist.Name)
	}
	// parent_id 正确回填
	if city.ParentID != prov.ID || dist.ParentID != city.ID {
		t.Fatalf("parent links wrong: city.parent=%d(want %d) dist.parent=%d(want %d)",
			city.ParentID, prov.ID, dist.ParentID, city.ID)
	}

	// 幂等: 再次添加不应重复创建
	created2, err := db.AddRegionByCivilCode(ctx, "440106")
	if err != nil || created2 != 0 {
		t.Fatalf("re-add should create 0, got %d err=%v", created2, err)
	}
}

func TestGBGroupCRUDAndAttach(t *testing.T) {
	ctx := context.Background()
	db := newCatalogTestDB(t)

	// 业务分组(215) -> 虚拟组织(216)
	bg := &GBGroup{DeviceID: "34020000002150000001", Name: "业务分组A", BusinessGroup: "34020000002150000001"}
	if _, err := db.CreateGBGroup(ctx, bg); err != nil {
		t.Fatalf("create business group: %v", err)
	}
	vo := &GBGroup{
		DeviceID:       "34020000002160000001",
		Name:           "虚拟组织1",
		BusinessGroup:  bg.DeviceID,
		ParentDeviceID: bg.DeviceID,
	}
	if _, err := db.CreateGBGroup(ctx, vo); err != nil {
		t.Fatalf("create virtual org: %v", err)
	}

	all, err := db.ListGBGroupsByBusinessGroup(ctx, bg.DeviceID)
	if err != nil || len(all) != 2 {
		t.Fatalf("business group members = %+v, err=%v", all, err)
	}

	// 挂接通道到虚拟组织
	seedChannel(t, db, "34020000001320000002", "34020000001320000002")
	key := ChannelKey{DeviceID: "34020000001320000002", ChannelID: "34020000001320000002"}
	if err := db.AttachChannelsToGroup(ctx, vo.DeviceID, bg.DeviceID, []ChannelKey{key}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	chs, err := db.ListChannelsByParentID(ctx, vo.DeviceID)
	if err != nil || len(chs) != 1 {
		t.Fatalf("list by parent id = %+v, err=%v", chs, err)
	}
	if chs[0].BusinessGroupID != bg.DeviceID {
		t.Fatalf("business_group_id = %q, want %q", chs[0].BusinessGroupID, bg.DeviceID)
	}

	// 删除整个业务分组 -> 通道置空
	_ = db.DetachChannelsFromBusinessGroup(ctx, bg.DeviceID)
	if err := db.DeleteGBGroups(ctx, []int64{bg.ID, vo.ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n, _ := db.CountChannelsByParentID(ctx, vo.DeviceID); n != 0 {
		t.Fatalf("channels still attached: %d", n)
	}
}
