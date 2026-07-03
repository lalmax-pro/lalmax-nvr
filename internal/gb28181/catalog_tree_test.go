package gb28181

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

const (
	testPlatformID = "44010000002000000001"
	testDevID      = "34020000001110000001"
	testBG         = "34020000002150000001"
	testVO         = "34020000002160000001"
	testRegionCh   = "34020000001320000001"
	testGroupCh    = "34020000001320000002"
)

func newCatalogTestPlatform(t *testing.T) *Platform {
	t.Helper()
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "cat.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Init(ctx); err != nil {
		t.Fatalf("init db: %v", err)
	}

	// 行政区划: 广东省 -> 广州市
	if _, err := db.CreateGBRegion(ctx, &storage.GBRegion{DeviceID: "4401", Name: "广东省"}); err != nil {
		t.Fatalf("region prov: %v", err)
	}
	if _, err := db.CreateGBRegion(ctx, &storage.GBRegion{DeviceID: "440100", Name: "广州市", ParentDeviceID: "4401"}); err != nil {
		t.Fatalf("region city: %v", err)
	}
	// 业务分组(215) -> 虚拟组织(216)
	if _, err := db.CreateGBGroup(ctx, &storage.GBGroup{DeviceID: testBG, Name: "业务分组", BusinessGroup: testBG}); err != nil {
		t.Fatalf("group bg: %v", err)
	}
	if _, err := db.CreateGBGroup(ctx, &storage.GBGroup{DeviceID: testVO, Name: "虚拟组织", BusinessGroup: testBG, ParentDeviceID: testBG}); err != nil {
		t.Fatalf("group vo: %v", err)
	}
	// 通道: 一个挂区划, 一个挂分组
	if err := db.UpsertGB28181Device(ctx, &storage.GB28181DeviceRow{DeviceID: testDevID, Name: "dev"}); err != nil {
		t.Fatalf("device: %v", err)
	}
	if err := db.BatchUpsertChannels(ctx, testDevID, []storage.GB28181ChannelRow{
		{DeviceID: testDevID, ChannelID: testRegionCh, Name: "区划通道", CivilCode: "440100"},
		{DeviceID: testDevID, ChannelID: testGroupCh, Name: "分组通道", ParentID: testVO, BusinessGroupID: testBG},
	}); err != nil {
		t.Fatalf("channels: %v", err)
	}

	return &Platform{store: db, Config: &PlatformConfig{DeviceGBID: testPlatformID}}
}

func TestBuildCatalogItems(t *testing.T) {
	p := newCatalogTestPlatform(t)
	items := p.buildCatalogItems([]PlatformChannel{
		{DeviceID: testDevID, ChannelID: testRegionCh},
		{DeviceID: testDevID, ChannelID: testGroupCh},
	})

	byID := map[string]catalogItem{}
	firstIdx := map[string]int{}
	for i, it := range items {
		byID[it.DeviceID] = it
		if _, dup := firstIdx[it.DeviceID]; !dup {
			firstIdx[it.DeviceID] = i
		}
	}

	// 行政区划父链
	if r, ok := byID["4401"]; !ok || r.ParentID != testPlatformID || r.Parental != 1 {
		t.Fatalf("region 4401 wrong: %+v (ok=%v)", r, ok)
	}
	if r, ok := byID["440100"]; !ok || r.ParentID != "4401" {
		t.Fatalf("region 440100 wrong: %+v (ok=%v)", r, ok)
	}
	// 区划通道挂在 440100 下, 带 CivilCode
	if c, ok := byID[testRegionCh]; !ok || c.ParentID != "440100" || c.CivilCode != "440100" || c.Parental != 0 {
		t.Fatalf("region channel wrong: %+v (ok=%v)", c, ok)
	}

	// 业务分组父链
	if g, ok := byID[testBG]; !ok || g.ParentID != testPlatformID || g.BusinessGroupID != testBG {
		t.Fatalf("business group wrong: %+v (ok=%v)", g, ok)
	}
	if g, ok := byID[testVO]; !ok || g.ParentID != testBG {
		t.Fatalf("virtual org wrong: %+v (ok=%v)", g, ok)
	}
	// 分组通道挂在虚拟组织下, 带 BusinessGroupID
	if c, ok := byID[testGroupCh]; !ok || c.ParentID != testVO || c.BusinessGroupID != testBG || c.Parental != 0 {
		t.Fatalf("group channel wrong: %+v (ok=%v)", c, ok)
	}

	// 顺序: 父节点先于子节点
	if firstIdx["4401"] > firstIdx["440100"] {
		t.Fatalf("region parent must precede child: %v", firstIdx)
	}
	if firstIdx[testBG] > firstIdx[testVO] {
		t.Fatalf("business group must precede virtual org: %v", firstIdx)
	}

	// 去重: 节点各只出现一次
	count := map[string]int{}
	for _, it := range items {
		count[it.DeviceID]++
	}
	for _, id := range []string{"4401", "440100", testBG, testVO} {
		if count[id] != 1 {
			t.Fatalf("node %s appeared %d times, want 1", id, count[id])
		}
	}
}

func TestBuildCatalogItemsUngrouped(t *testing.T) {
	p := newCatalogTestPlatform(t)
	ctx := context.Background()
	// 未分配区划/分组的通道
	if err := p.store.BatchUpsertChannels(ctx, testDevID, []storage.GB28181ChannelRow{
		{DeviceID: testDevID, ChannelID: "34020000001320000009", Name: "裸通道"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	items := p.buildCatalogItems([]PlatformChannel{
		{DeviceID: testDevID, ChannelID: "34020000001320000009"},
	})
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].ParentID != testPlatformID {
		t.Fatalf("ungrouped channel should hang under platform, got %+v", items[0])
	}
}

func TestBuildCatalogItemsEmpty(t *testing.T) {
	p := newCatalogTestPlatform(t)
	if items := p.buildCatalogItems(nil); len(items) != 0 {
		t.Fatalf("want empty, got %d", len(items))
	}
}

func TestBuildCatalogItemXML(t *testing.T) {
	xml := buildCatalogItemXML(catalogItem{
		DeviceID: "440100", Name: "广州市", ParentID: "4401", Parental: 1, Status: "ON",
	})
	for _, want := range []string{"<DeviceID>440100</DeviceID>", "<ParentID>4401</ParentID>", "<Parental>1</Parental>"} {
		if !strings.Contains(xml, want) {
			t.Fatalf("xml missing %q:\n%s", want, xml)
		}
	}
}
