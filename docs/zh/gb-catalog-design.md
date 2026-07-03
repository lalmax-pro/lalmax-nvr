# 国标设备通道（行政区划 & 业务分组）设计草案

本文档用于为 lalmax-nvr 设计一套符合 GB28181 标准的设备通道目录组织能力：**行政区划**（civil code 区划树）与 **业务分组**（业务分组 / 虚拟组织树）。设计参考成熟实现 [wvp-GB28181-pro](https://github.com/648540858/wvp-GB28181-pro) 的 `Region` / `Group` 模型，并结合 lalmax-nvr 现有架构落地。

## 背景

lalmax-nvr 同时扮演两种 GB28181 角色：

1. **作为 SIP 服务器（上级平台）**：下级设备注册上来，目录写入 `gb28181_devices` + `gb28181_channels`。通道表已经携带设备上报的 `civil_code` / `parent_id` / `business_group_id` 等完整国标字段，但这些字段目前只是被动存储，没有用于组织目录。
2. **作为下级平台（国标级联）**：通过 `internal/gb28181/platform.go` 的 `PlatformManager` 向上级平台注册并共享通道，相关数据在 `gb28181_platforms` + `gb28181_platform_channels`。

现有的「设备分组」(`device_groups` + `device_group_channels`) 是一个**与国标无关的本地归类功能**：自定义树（`parent_id / level / sort_order`），通道用 `device_id + channel_id` 多对多挂接，可以混合 GB 通道与本地摄像头。它解决的是「UI 上怎么把设备归类展示」，而不是「怎么按国标目录组织并上报」。

## 当前问题

### 1. 缺少国标标准目录树

GB28181 的目录有两套标准组织维度：

- **行政区划（CivilCode）**：基于 GB/T 2260 行政区划代码（如 `4401` → `440100` → `44010000`），编码长度递增表示层级。
- **业务分组（BusinessGroup）/ 虚拟组织（VirtualOrganization）**：20 位国标编号第 11-13 位类型码 `215` 表示业务分组（顶层），`216` 表示虚拟组织（可多级嵌套）。

lalmax-nvr 目前两者都没有建模，无法把通道池组织成上级平台期望的标准目录树。

### 2. 级联上报目录是扁平的

`platform.go` 的 `handleCatalogResponse` 当前把共享通道当作扁平列表逐条发出，每个通道 `<ParentID>` 都填平台自身 ID：

```
平台ID
 ├─ 通道A
 ├─ 通道B
 └─ 通道C
```

上级平台收到的是一堆挂在平台根下的通道，没有区划层级、没有分组层级。这是本设计**最主要的业务收益落点**。

### 3. `device_groups` 不应承载国标语义

如果硬把 `device_groups` 改造成国标目录，会破坏它现有的「本地自定义归类」定位（混合摄像头、自由命名、与编码无关）。正确做法是**新增一套独立的国标目录能力**，与 `device_groups` 并存、职责分离。

## 设计目标

1. 在通道池之上建立两棵 GB28181 标准目录树：行政区划、业务分组。
2. 通道可被批量「挂接」到区划（写 `civil_code`）和虚拟组织（写 `parent_id` + `business_group_id`），或解绑（置空）。
3. 级联上报时按这两棵树构造带层级的 Catalog，而非扁平列表。
4. 前端独立成「国标设备通道」页：左侧双视图树（区划 / 分组），右侧通道列表 + 批量挂接/解绑。
5. 不改动现有 `device_groups`，二者职责分离。

## 与 wvp 的现状对比

| 维度 | wvp | lalmax-nvr 现状 |
|---|---|---|
| 通道池 | `wvp_common_channel`，带 `gb_civil_code` / `gb_parent_id` / `gb_business_group_id` | `gb28181_channels` **已有** `civil_code` / `parent_id` / `business_group_id`（仅被动存储） |
| 行政区划 | `wvp_common_region` 树 | ❌ 无 |
| 业务分组 | `wvp_common_group` 树（215 / 216） | ❌ 无 |
| 本地归类 | —— | `device_groups`（非国标，UI 归类） |
| 级联上报 | 区划/分组节点 + 通道按树推送 | 扁平通道列表，`ParentID = 平台ID` |
| 编码标准库 | `CivilCodeUtil`（内置 GB/T 2260 全量编码文件） | ❌ 无 |

## 数据模型

新增两张目录表（对应 wvp 的 `wvp_common_region` / `wvp_common_group`），SQLite：

```sql
-- 行政区划
CREATE TABLE IF NOT EXISTS gb28181_regions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id TEXT NOT NULL UNIQUE,      -- 区划编码 GB/T 2260, 如 4401 / 440100 / 44010000
    name TEXT NOT NULL,
    parent_device_id TEXT DEFAULT '',
    parent_id INTEGER DEFAULT 0,         -- 冗余自增父ID, 加速建树
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_gb_regions_parent ON gb28181_regions(parent_id);
CREATE INDEX IF NOT EXISTS idx_gb_regions_parent_device ON gb28181_regions(parent_device_id);

-- 业务分组 / 虚拟组织 (统一一张表, 用类型码区分)
CREATE TABLE IF NOT EXISTS gb28181_groups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id TEXT NOT NULL,             -- 20位国标编号, 11-13位=215(业务分组)或216(虚拟组织)
    name TEXT NOT NULL,
    parent_device_id TEXT DEFAULT '',    -- 上级216节点
    parent_id INTEGER DEFAULT 0,
    business_group TEXT NOT NULL,        -- 所属215业务分组编号 (215节点指向自身)
    civil_code TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(device_id, business_group)
);
CREATE INDEX IF NOT EXISTS idx_gb_groups_business ON gb28181_groups(business_group);
CREATE INDEX IF NOT EXISTS idx_gb_groups_parent ON gb28181_groups(parent_id);
```

**通道侧不新建表**：`gb28181_channels` 已有 `civil_code` / `parent_id` / `business_group_id`，直接复用。

- 挂接区划：`UPDATE gb28181_channels SET civil_code = ? WHERE (device_id, channel_id) IN (...)`
- 挂接分组：`UPDATE gb28181_channels SET parent_id = ?, business_group_id = ? WHERE ...`
- 解绑：对应字段置空。

> 通道标识用 `device_id + channel_id` 复合键（lalmax-nvr 通道无单一自增主键），与现有 `device_group_channels` 标识方式保持一致。

### 业务分组节点类型规则（参考 wvp `Group.getInstance` / `GbCode`）

20 位编号第 11-13 位类型码：

- `215`（业务分组顶层）：`business_group` 指向自身 `device_id`，无 `parent_device_id`。
- `216`（虚拟组织）：`business_group` 指向所属 215 节点，`parent_device_id` 指向上级 216 节点（顶层 216 的父为对应 215）。

## 后端实现（Go）

沿用现有分层 `handlers_*.go → storage/db_*.go`：

- `internal/storage/db_gb_region.go`、`internal/storage/db_gb_group.go`
  注意：现有 `db_group.go` 是本地分组，避免命名冲突，新文件用 `db_gb_*` 前缀。
  内容：建表、CRUD、`ListRegionTree(parentID)` / `ListGroupTree(parentID, businessGroup)`、`SyncRegionsFromChannels()`、通道挂接/解绑。
- `internal/api/handlers_gb_region.go`、`internal/api/handlers_gb_group.go`：HTTP handler。
- `internal/gb28181/codeutil.go`：`DecodeGBCode(code)`（拆 20 位为 centerCode / industryCode / typeCode / netCode / sn）+ 类型码判断，对应 wvp 的 `GbCode`。

### API 设计（挂在 `/api/gb28181` 下）

```
# 行政区划
GET    /api/gb28181/regions/tree?parent=&has_channel=    区划树(可含通道叶子)
POST   /api/gb28181/regions                              新建区划
PUT    /api/gb28181/regions/{id}                         改名/改编号
DELETE /api/gb28181/regions/{id}                         删(级联子节点, 通道civil_code置空)
POST   /api/gb28181/regions/sync                         从通道反向同步区划

# 业务分组
GET    /api/gb28181/groups/tree?parent=&has_channel=     分组树
POST   /api/gb28181/groups                               新建(215或216, 按编号类型码分流)
PUT    /api/gb28181/groups/{id}                          改名/改编号
DELETE /api/gb28181/groups/{id}                          删(级联子节点, 通道置空)

# 通道挂接 (复合键 device_id + channel_id)
POST   /api/gb28181/channels/region    {civil_code, channels: [{device_id, channel_id}]}
DELETE /api/gb28181/channels/region    {civil_code?, channels: [...]}
POST   /api/gb28181/channels/group     {parent_id, business_group, channels: [...]}
DELETE /api/gb28181/channels/group     {channels: [...]}
```

### 与 wvp 实现的关键差异（落地注意点）

1. **复合键 vs 自增ID**：wvp 通道有自增 `gb_id`，挂接走 ID 列表；lalmax-nvr 用 `device_id + channel_id`，所有 mapper 按复合键写。
2. **单库 vs 多库**：wvp 用 MyBatis 多数据库方言；lalmax-nvr 只有 SQLite，建树直接在 Go 里递归（可参考现有 `buildGroupTree`），SQL 大幅简化。
3. **事件通知**：wvp 有完整事件总线，节点增删改会主动向上级平台发 Catalog 通知。lalmax-nvr 第一期**可不主动推**，让上级平台下次查询 Catalog 时自然拿到新树，成本最低；后续再补主动通知。
4. **删除语义（参考 wvp `RegionServiceImpl.deleteByDeviceId` / `GroupServiceImpl.delete`）**：删区划/分组节点时，递归收集子树，先把引用这些节点的通道字段置空，再删节点；删 215 业务分组要连带删除其下全部 216 虚拟组织。

## 与级联上报结合（核心价值）

改造 `internal/gb28181/platform.go` 的 `handleCatalogResponse`：

当前流程（扁平）：

```go
channels := pm.GetSharedChannels(p.Config.ID)
for _, ch := range channels {
    p.sendCatalogMessage(recipient, sn, cfg, &ch) // ParentID 恒为平台ID
}
```

新流程（按树）：

1. 取共享通道集合。
2. 收集这些通道涉及的 `civil_code` / `business_group` / `parent_id`，从 `gb28181_regions` / `gb28181_groups` 查出对应节点及其**父链**。
3. 参照 wvp `CommonGBChannel.build(Region)` / `build(Group)`，把**区划节点、业务分组节点、虚拟组织节点也作为 Catalog `<Item>`** 发出：
   - 区划 Item：`<ParentID>` 指向上级区划。
   - 业务分组(215) / 虚拟组织(216) Item：`<ParentID>` 指向上级分组节点，`<BusinessGroupID>` 填所属业务分组。
   - 通道 Item：`<ParentID>` 指向其虚拟组织（分组视图）或区划（区划视图）。
4. 更新 `SumNum` / `DeviceList Num`，按节点+通道总数计。

效果：

```
平台ID
 └─ 广东省(4401...)
     └─ 广州市(440100...)
         ├─ 大门通道
         └─ 虚拟组织(...216...)
             └─ 球机通道
```

## 前端「国标设备通道」页（Svelte）

新增 `web/src/routes/GB28181Channels.svelte` + `web/src/lib/api/gbCatalog.ts`，布局参考 wvp 的 `views/channel`：

```
┌─────────────┬──────────────────────────────────────┐
│ [行政区划▾] │  搜索  在线▾  类型▾      [批量挂接][解绑]│
│  业务分组    │ ┌──────────────────────────────────┐ │
│             │ │☐ 通道名   编号   在线  区划  分组  │ │
│ ▸ 广东省     │ │☐ 大门     3401.. ●    未分配 ...   │ │
│   ▸ 广州市   │ │...                                │ │
│     · 大门   │ └──────────────────────────────────┘ │
└─────────────┴──────────────────────────────────────┘
```

- 左树两个 tab：「行政区划」「业务分组」，调 `*/tree?has_channel=true`，**懒加载**子节点（点击节点才拉下一层，与 wvp 一致）。
- 右侧通道列表按选中节点过滤（`civil_code=` 或 `parent_id=`），勾选后批量挂接 / 解绑。
- 复用现有 `GB28181Devices.svelte` 的通道列表与播放入口组件。
- 在 `web/src/routes` 注册新路由，导航中与「设备分组」「国标设备」并列。

## 与现有 `device_groups` 的关系

二者并存，定位区分：

| 功能 | 定位 | 是否参与国标级联 |
|---|---|---|
| 国标设备通道页（新） | GB28181 标准目录（区划 + 业务分组） | ✅ 影响级联上报目录 |
| 设备分组（旧 `DeviceGroups.svelte`） | 本地自定义收藏 / 视图，可混合摄像头 | ❌ 纯本地 |

## 实施阶段

1. **P1 数据 + 挂接**：建两张表、`DecodeGBCode`、区划/分组 CRUD、通道挂接/解绑 API、`regions/sync` 反向同步。
2. **P2 前端页**：`GB28181Channels.svelte` 双树 + 批量挂接。
3. **P3 级联落地**：改 `handleCatalogResponse` 按树构造 Catalog（核心收益）。
4. **P4 增强**：引入 GB/T 2260 编码文件做自动补全与层级描述（移植 wvp `CivilCodeUtil`）。

## 参考

- wvp `Region` / `RegionServiceImpl` / `RegionMapper`：行政区划模型与建树、同步、删除语义。
- wvp `Group` / `GroupServiceImpl` / `GbCode`：业务分组 215/216 类型规则与树维护。
- wvp `GbChannelServiceImpl.addChannelToRegion / addChannelToGroup`：通道挂接/解绑与上报通知。
- wvp `CommonGBChannel.build(Region/Group)`：把目录节点构造成 Catalog Item 的方式。
