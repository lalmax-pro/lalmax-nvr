package gb28181

import (
	"context"
	"fmt"
	"strings"
)

// catalogItem 表示上报给上级平台的一个 Catalog <Item>。
// Parental=1 为目录节点(行政区划/业务分组/虚拟组织), Parental=0 为通道叶子。
type catalogItem struct {
	DeviceID        string
	Name            string
	ParentID        string
	CivilCode       string
	BusinessGroupID string
	Parental        int
	Status          string
}

// buildCatalogItems 根据共享通道及其挂接的行政区划/业务分组, 组装带层级的 Catalog 条目。
//
// 顺序保证父节点先于子节点出现; 行政区划与业务分组节点全局去重, 仅出现一次。
// 通道叶子的 ParentID 优先取虚拟组织, 其次行政区划, 都没有则挂在平台根下。
func (p *Platform) buildCatalogItems(channels []PlatformChannel) []catalogItem {
	ctx := context.Background()
	platformID := p.Config.DeviceGBID

	var items []catalogItem
	regionSeen := map[string]bool{}
	groupSeen := map[string]bool{}

	parentOrPlatform := func(parent string) string {
		if parent == "" {
			return platformID
		}
		return parent
	}

	// 补全行政区划节点及其父链 (root->leaf 顺序追加)
	addRegionChain := func(code string) {
		var chain []catalogItem
		for code != "" && !regionSeen[code] {
			r, err := p.store.GetGBRegionByDeviceID(ctx, code)
			if err != nil || r == nil {
				break
			}
			regionSeen[code] = true
			chain = append(chain, catalogItem{
				DeviceID: r.DeviceID,
				Name:     r.Name,
				ParentID: parentOrPlatform(r.ParentDeviceID),
				Parental: 1,
				Status:   "ON",
			})
			code = r.ParentDeviceID
		}
		for i := len(chain) - 1; i >= 0; i-- {
			items = append(items, chain[i])
		}
	}

	// 补全虚拟组织节点及其父链, 直到业务分组(215) (root->leaf 顺序追加)
	addGroupChain := func(deviceID, businessGroup string) {
		var chain []catalogItem
		cur := deviceID
		for cur != "" && !groupSeen[cur] {
			g, err := p.store.GetGBGroupByDeviceID(ctx, cur, businessGroup)
			if err != nil || g == nil {
				break
			}
			groupSeen[cur] = true
			chain = append(chain, catalogItem{
				DeviceID:        g.DeviceID,
				Name:            g.Name,
				ParentID:        parentOrPlatform(g.ParentDeviceID),
				BusinessGroupID: g.BusinessGroup,
				Parental:        1,
				Status:          "ON",
			})
			cur = g.ParentDeviceID
		}
		// 兜底: 父链未走到 215 时, 单独补上业务分组节点
		if businessGroup != "" && !groupSeen[businessGroup] {
			if bg, err := p.store.GetGBBusinessGroup(ctx, businessGroup); err == nil && bg != nil {
				groupSeen[businessGroup] = true
				chain = append(chain, catalogItem{
					DeviceID:        bg.DeviceID,
					Name:            bg.Name,
					ParentID:        platformID,
					BusinessGroupID: bg.BusinessGroup,
					Parental:        1,
					Status:          "ON",
				})
			}
		}
		for i := len(chain) - 1; i >= 0; i-- {
			items = append(items, chain[i])
		}
	}

	for _, ch := range channels {
		var civil, parentOrg, bg string
		if chRow, err := p.store.GetGB28181Channel(ctx, ch.DeviceID, ch.ChannelID); err == nil && chRow != nil {
			civil = chRow.CivilCode
			parentOrg = chRow.ParentID
			bg = chRow.BusinessGroupID
		}
		if civil != "" {
			addRegionChain(civil)
		}
		if parentOrg != "" && bg != "" {
			addGroupChain(parentOrg, bg)
		}

		channelID := ch.CustomID
		if channelID == "" {
			channelID = ch.ChannelID
		}
		name := ch.CustomName
		if name == "" {
			name = ch.ChannelID
		}

		parentID := platformID
		if parentOrg != "" {
			parentID = parentOrg
		} else if civil != "" {
			parentID = civil
		}

		items = append(items, catalogItem{
			DeviceID:        channelID,
			Name:            name,
			ParentID:        parentID,
			CivilCode:       civil,
			BusinessGroupID: bg,
			Parental:        0,
			Status:          "ON",
		})
	}

	return items
}

// buildCatalogItemXML 生成单个 <Item> 块。
func buildCatalogItemXML(it catalogItem) string {
	var sb strings.Builder
	sb.WriteString("<Item>\n")
	sb.WriteString(fmt.Sprintf("<DeviceID>%s</DeviceID>\n", it.DeviceID))
	sb.WriteString(fmt.Sprintf("<Name>%s</Name>\n", it.Name))
	if it.ParentID != "" {
		sb.WriteString(fmt.Sprintf("<ParentID>%s</ParentID>\n", it.ParentID))
	}
	if it.CivilCode != "" {
		sb.WriteString(fmt.Sprintf("<CivilCode>%s</CivilCode>\n", it.CivilCode))
	}
	if it.BusinessGroupID != "" {
		sb.WriteString(fmt.Sprintf("<BusinessGroupID>%s</BusinessGroupID>\n", it.BusinessGroupID))
	}
	sb.WriteString(fmt.Sprintf("<Parental>%d</Parental>\n", it.Parental))
	sb.WriteString("<RegisterWay>1</RegisterWay>\n")
	sb.WriteString("<Secrecy>0</Secrecy>\n")
	status := it.Status
	if status == "" {
		status = "ON"
	}
	sb.WriteString(fmt.Sprintf("<Status>%s</Status>\n", status))
	sb.WriteString("</Item>")
	return sb.String()
}
