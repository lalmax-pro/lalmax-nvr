# GitHub Wiki 知识库建设与发布

本项目采用 **主仓库保存正文、构建生成 Wiki、GitHub 独立 Wiki 仓库展示** 的方式。这样既能评审文档变更，也能利用 GitHub Wiki 的首页、侧边栏、页脚和页面历史。

知识库地址（完成首次初始化后可用）：

```text
https://github.com/lalmax-pro/lalmax-nvr/wiki
```

## 1. 为什么需要发布步骤

GitHub Wiki 不是对主仓库 `docs/` 目录的自动渲染。它是独立的 Git 仓库，远程地址为：

```text
https://github.com/lalmax-pro/lalmax-nvr.wiki.git
```

本仓库的源文件分工：

| 路径 | 用途 |
|------|------|
| `docs/zh/*.md` | 技术规范和专题正文，唯一事实源 |
| `docs/wiki/Home.md` | Wiki 首页 |
| `docs/wiki/_Sidebar.md` | 自定义侧边栏 |
| `docs/wiki/_Footer.md` | 自定义页脚 |
| `scripts/build_wiki.py` | 页面映射、Wiki 内链改写、仓库文件和图片链接转换 |
| `scripts/check_docs.py` | 主仓库 Markdown 本地链接检查 |
| `.github/workflows/wiki.yml` | 主分支变更后的同步 |

生成器使用显式页面映射，例如 `docs/zh/architecture.md` 发布为 `Architecture.md`。正文中的 `configuration.md` 等链接会改写为 Wiki 页面；代码、配置等链接指向主仓库；图片改用 `raw.githubusercontent.com`，避免 Wiki 独立仓库中的相对路径失效。

## 2. 一次性初始化

### 2.1 启用并创建初始页面

1. 打开仓库。如果没有 **Wiki** 标签，先在仓库设置中确认 Wikis 功能已启用。
2. 进入 **Wiki**，创建名为 **Home** 的初始页面并保存。内容可以是临时占位文本，后续同步会替换。
3. 确认能访问 `https://github.com/lalmax-pro/lalmax-nvr/wiki`。

**必须先创建初始页面。** GitHub 官方文档明确说明，创建初始页面后才能克隆 Wiki；因此不能指望首次 Actions 在完全未初始化的 Wiki 上正常克隆。

### 2.2 选择发布方式

- **自动发布（推荐）**：完成初始化后，把本仓库变更合并到 `main`。工作流会在文档、生成器或自身变化时同步，也可在 Actions 中手工运行 **Publish GitHub Wiki**。
- **手动发布**：适合首次验收、 fork 或不使用 Actions 的环境，按第 4 节执行。

本次代码变更只提供发布能力，不会在开发机上自动创建线上 Wiki 或推送内容。

## 3. 自动发布

工作流触发条件：

- 推送到 `main`，且变更涉及 `docs/zh`、`docs/wiki`、生成/检查脚本或 Wiki 工作流；
- 在 GitHub Actions 中手动 `workflow_dispatch`。

执行顺序：

1. 检查主仓库本地 Markdown 链接。
2. 生成 Wiki 页面到临时目录。
3. 克隆 `<repository>.wiki.git` 的默认分支。
4. 覆盖同名受管页面，提交并推送。

工作流需要 `contents: write`。它只复制生成页面，**不删除 Wiki 中未由本项目管理的手工页面**；但同名页面的网页端修改会被下一次同步覆盖。删除受管页面时应在映射、源文件和 Wiki 仓库中一起处理。

若 Actions 报 `Repository not found` 且目标是 `.wiki.git`，首先检查是否完成初始 Home 页面，再检查 Workflow 权限和仓库可见性；不要把它当作主仓库 checkout 失败处理。

## 4. 本地预览与手动发布

### 4.1 只生成，不推送

在仓库根目录运行：

```bash
python3 scripts/check_docs.py
python3 scripts/build_wiki.py --output tmp/wiki
```

`tmp/` 已被 `.gitignore` 排除。生成结果可直接打开检查；这一步骤不访问 GitHub，也不修改线上 Wiki。

### 4.2 手动推送

完成第 2 节初始化后：

```bash
git clone git@github.com:lalmax-pro/lalmax-nvr.wiki.git tmp/wiki-repo
cp -R tmp/wiki/. tmp/wiki-repo/
git -C tmp/wiki-repo status --short
git -C tmp/wiki-repo add -A
git -C tmp/wiki-repo commit -m "Sync knowledge base from docs"
git -C tmp/wiki-repo push
```

提交和推送是对线上 Wiki 的写操作，应由维护者在确认生成内容后执行。Wiki 的分支可以创建，但 GitHub 只展示推送到 Wiki **默认分支** 的内容；不要假定其分支名一定是主仓库的 `main`。

也可以使用 HTTPS 克隆：

```bash
git clone https://github.com/lalmax-pro/lalmax-nvr.wiki.git tmp/wiki-repo
```

## 5. 页面组织规则

1. `Home.md` 是首页；`_Sidebar.md` 和 `_Footer.md` 是 GitHub 约定的特殊文件。
2. Wiki 文件名决定页面标题；不要使用 GitHub 不支持的 `\\ / : * ? " < > |` 字符。
3. 受管页面使用稳定的英文文件名，中文标题保留在正文，避免改链接时破坏外部引用。
4. Wiki 内部链接使用生成后的页面名，例如 `[配置规范](Configuration)`；源文档仍使用正常相对路径。
5. 新增受管页面时，同时更新 `scripts/build_wiki.py` 的 `PAGE_MAP`、Home 和 Sidebar。
6. 不要直接编辑线上同名页面；在主仓库提交修改，让评审、测试和页面历史关联到同一代码版本。

## 6. 可见性、搜索与 GitHub Pages 的选择

- 公共仓库的 Wiki 也是公开的；私有仓库 Wiki 只对有仓库访问权的人开放。不要把真实凭据、内部地址或敏感运维数据发布进来。
- 默认只有有仓库写权限的用户可编辑 Wiki；公共 Wiki 是否允许所有 GitHub 用户编辑应单独审查。本项目同步模式建议限制为维护者写入。
- GitHub 官方说明：搜索引擎只索引达到其条件的 Wiki（500 或更多 stars，且禁止公众编辑）；Wiki 还有约 5000 文件的软限制。
- 如果目标是公开搜索、品牌官网或完整文档站点，应使用/扩展现有 GitHub Pages，而不是把 Wiki 当作 SEO 站点。当前 Pages 工作流主要部署产品站和 OpenAPI；Wiki 适合作为维护者主导的项目知识库。

## 7. 验收清单

1. 本地 `check_docs.py` 无缺失目标。
2. 本地生成页数与 `PAGE_MAP` 一致，Home/Sidebar/Footer 均存在。
3. 核心页面间链接不再含指向 `docs/zh` 的失效相对地址。
4. 截图和配置/代码链接在 Wiki 页面可访问。
5. Mermaid 图、表格、代码块和中文锚点在实际 GitHub Wiki 中检查渲染。
6. Actions 使用默认 Wiki 分支成功推送；重复运行无内容变化时不产生空提交。
7. 线上检查 Home、侧边栏、至少一篇核心规范、一篇含图片专题和 OpenAPI 外链。

## 8. 官方依据

- [About wikis](https://docs.github.com/en/communities/documenting-your-project-with-wikis/about-wikis)：Wiki 的可见性、编辑权限、搜索索引和文件限制。
- [Adding or editing wiki pages](https://docs.github.com/en/communities/documenting-your-project-with-wikis/adding-or-editing-wiki-pages)：独立 Git 仓库、初始页面、克隆 URL、默认分支和文件名规则。
- [Creating a footer or sidebar for your wiki](https://docs.github.com/en/communities/documenting-your-project-with-wikis/creating-a-footer-or-sidebar-for-your-wiki)：`_Footer` 与 `_Sidebar` 约定。
