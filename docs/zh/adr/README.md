# 架构决策记录（ADR）

本目录记录 lalmax-nvr 的重大架构决策。每条记录说明决策背景、备选方案、最终选择和已知权衡。

> ADR 是决策快照，不随实现迭代更新。若决策被推翻，新建一条"Superseded"状态的记录，并交叉引用旧记录。

---

## 记录索引

| 编号 | 标题 | 状态 | 日期 |
|------|------|------|------|
| [ADR-001](adr-001-embedded-media-engine.md) | 嵌入式媒体引擎（embedded mode）为默认模式 | Accepted | 2026-10 |
| [ADR-002](adr-002-dual-transcode-backend.md) | 转码双后端（FFmpeg / GStreamer）共享管理器 | Accepted | 2026-10 |
| [ADR-003](adr-003-job-sharing-model.md) | 相同源+模板的转码任务共享物理输出 | Accepted | 2026-10 |
| [ADR-004](adr-004-cgo-free.md) | 保持 CGO_ENABLED=0，媒体工具以子进程运行 | Accepted | 2026-10 |
| [ADR-005](adr-005-sqlite-default-db.md) | SQLite 为默认数据库，支持 PostgreSQL / MySQL 切换 | Accepted | 2026-10 |

---

## 状态说明

| 状态 | 含义 |
|------|------|
| Proposed | 已提出，尚未确认 |
| Accepted | 已确认并执行 |
| Superseded | 已被新决策取代，见交叉引用 |
| Deprecated | 功能已弃用 |

---

## 新增记录

新建 ADR 文件命名为 `adr-NNN-<kebab-title>.md`，使用以下模板：

```markdown
# ADR-NNN：标题

- **状态**：Proposed / Accepted
- **日期**：YYYY-MM-DD
- **关联**：Issue #xx，PR #xx

## 背景

描述需要做决策的问题或约束。

## 备选方案

列出考虑过的方案及其优缺点。

## 决策

选择了哪个方案，理由是什么。

## 权衡与已知限制

这个决策带来的代价、约束或需要后续处理的问题。
```
