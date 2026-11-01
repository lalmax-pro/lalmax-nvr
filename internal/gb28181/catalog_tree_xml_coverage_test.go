package gb28181

import (
	"context"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestBuildCatalogItemXMLOptionalFieldsAndDefaultStatus(t *testing.T) {
	xml := buildCatalogItemXML(catalogItem{
		DeviceID:        "34020000001320000001",
		Name:            "Cam",
		ParentID:        "440100",
		CivilCode:       "440100",
		BusinessGroupID: "34020000002150000001",
		Parental:        0,
	})
	for _, want := range []string{
		"<DeviceID>34020000001320000001</DeviceID>",
		"<Name>Cam</Name>",
		"<ParentID>440100</ParentID>",
		"<CivilCode>440100</CivilCode>",
		"<BusinessGroupID>34020000002150000001</BusinessGroupID>",
		"<Parental>0</Parental>",
		"<RegisterWay>1</RegisterWay>",
		"<Secrecy>0</Secrecy>",
		"<Status>ON</Status>",
	} {
		require.Contains(t, xml, want)
	}

	minimal := buildCatalogItemXML(catalogItem{DeviceID: "id", Name: "n", Parental: 1, Status: "OFF"})
	require.Contains(t, minimal, "<Status>OFF</Status>")
	require.NotContains(t, minimal, "<ParentID>")
	require.NotContains(t, minimal, "<CivilCode>")
	require.NotContains(t, minimal, "<BusinessGroupID>")
	require.True(t, strings.HasPrefix(minimal, "<Item>\n"))
	require.True(t, strings.HasSuffix(minimal, "</Item>"))
}

func TestBuildCatalogItemsUsesCustomIDAndName(t *testing.T) {
	p := newCatalogTestPlatform(t)
	items := p.buildCatalogItems([]PlatformChannel{{
		DeviceID:   testDevID,
		ChannelID:  testRegionCh,
		CustomID:   "custom-channel-id",
		CustomName: "自定义通道",
	}})
	require.NotEmpty(t, items)
	leaf := items[len(items)-1]
	require.Equal(t, "custom-channel-id", leaf.DeviceID)
	require.Equal(t, "自定义通道", leaf.Name)
	require.Equal(t, "440100", leaf.ParentID)
	require.Equal(t, "440100", leaf.CivilCode)
	require.Equal(t, 0, leaf.Parental)
}

func TestBuildCatalogItemsUnknownCivilCode(t *testing.T) {
	p := newCatalogTestPlatform(t)
	ctx := context.Background()
	unknownCh := "34020000001320000008"
	require.NoError(t, p.store.BatchUpsertChannels(ctx, testDevID, []storage.GB28181ChannelRow{
		{DeviceID: testDevID, ChannelID: unknownCh, Name: "未知区划", CivilCode: "999999"},
	}))

	items := p.buildCatalogItems([]PlatformChannel{{DeviceID: testDevID, ChannelID: unknownCh}})
	require.Len(t, items, 1)
	require.Equal(t, unknownCh, items[0].DeviceID)
	require.Equal(t, "999999", items[0].ParentID)
	require.Equal(t, "999999", items[0].CivilCode)
}

func TestBuildCatalogItemsParentOrgWithoutBusinessGroup(t *testing.T) {
	p := newCatalogTestPlatform(t)
	ctx := context.Background()
	chID := "34020000001320000007"
	require.NoError(t, p.store.BatchUpsertChannels(ctx, testDevID, []storage.GB28181ChannelRow{
		{DeviceID: testDevID, ChannelID: chID, Name: "仅父组织", ParentID: testVO},
	}))

	items := p.buildCatalogItems([]PlatformChannel{{DeviceID: testDevID, ChannelID: chID}})
	require.Len(t, items, 1)
	require.Equal(t, testVO, items[0].ParentID)
	require.Empty(t, items[0].BusinessGroupID)
}

func TestBuildCatalogItemsMissingChannelRowHangsUnderPlatform(t *testing.T) {
	p := newCatalogTestPlatform(t)
	items := p.buildCatalogItems([]PlatformChannel{{
		DeviceID:  testDevID,
		ChannelID: "34020000001320000099",
	}})
	require.Len(t, items, 1)
	require.Equal(t, testPlatformID, items[0].ParentID)
	require.Equal(t, "34020000001320000099", items[0].Name)
}
