# lalmax-nvr 产品网站

本目录包含 lalmax-nvr 的产品首页和 OpenAPI 文档站点。GitHub Pages 部署地址：

- 产品首页：https://lalmax-pro.github.io/lalmax-nvr/
- OpenAPI：https://lalmax-pro.github.io/lalmax-nvr/docs/
- 技术知识库：https://github.com/lalmax-pro/lalmax-nvr/wiki

## 技术结构

- Next.js 16 / React 19，产品页使用静态导出。
- `app/page.tsx`：首页内容、能力、架构、快速开始和 Wiki 入口。
- `app/globals.css`：页面样式与响应式布局。
- `app/docs/`：基于 Scalar 的 OpenAPI 页面。
- `public/`：本地图片、图标和供页面加载的 OpenAPI 文件。
- `worker/`、`vite.config.ts`：可选 vinext / Cloudflare 开发和渲染链路。

页面素材必须来自本目录的本地文件或仓库中已有的真实产品截图，不使用占位图片服务。

## 本地开发

要求 Node.js `>=22.13.0` 和 npm。

```bash
npm ci
npm run dev
```

## 验证

```bash
npm run lint
npm test
```

`npm test` 会构建 vinext 产物并检查服务端渲染、元数据和本地社交图片。

按 GitHub Pages 的实际 base path 验证静态导出：

```bash
NEXT_PUBLIC_BASE_PATH=/lalmax-nvr \
NEXT_PUBLIC_SITE_URL=https://lalmax-pro.github.io/lalmax-nvr \
npm run build:pages
```

输出目录为 `out/`。

## OpenAPI 同步

`build`、`build:pages` 和 `dev` 会先执行：

```bash
npm run sync-openapi
```

该命令把 `../internal/docsportal/openapi.yaml` 复制到 `public/openapi.yaml`。接口契约应在主文档源中修改，不要只改 Pages 的副本。

## 内容维护

产品文案描述当前已经实现的能力，不应把实验功能、目标性能或路线图写成确定承诺：

- 不写未经基准验证的延迟、吞吐和兼容性数字。
- “核心单二进制”不排除可选 FFmpeg / GStreamer 工具。
- 设备接入应区分拉流、推流、SIP 信令和内容源发布。
- 添加设备或发布流不等于自动录像；录像计划绑定 `stream_id`。
- 详细规则链接到 GitHub Wiki 的对应规范，不在产品页复制整份技术文档。

## 发布

`.github/workflows/pages.yml` 在 `main` 分支的 `site/**` 或该工作流变化时构建并部署 Pages，也支持手动运行。构建环境显式设置 `NEXT_PUBLIC_BASE_PATH` 和 `NEXT_PUBLIC_SITE_URL`。
