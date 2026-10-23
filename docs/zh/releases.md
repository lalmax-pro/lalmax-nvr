# 发布与自动构建

`Build` 工作流支持两种发版入口：推送 `v1.0.1` / `V1.0.1` 形式的 tag，或在 GitHub 发布该 tag 的 Release（`published`，包含预发布）。tag 必须指向包含此工作流的提交；修改 main 不会修复历史 tag 内的旧工作流。

```sh
git tag -a V1.0.1 <已验证的提交> -m "Release V1.0.1"
git push origin refs/tags/V1.0.1
```

也可以在 GitHub Releases 页面选择该提交、填写新 tag 并发布。不要同时用两种入口重复发起同一版本；同一 tag 的工作流会串行运行，不会在上传产物中途互相取消。Release 页面创建新 tag 可能同时产生 push 与 published 事件；后一个任务会检查同一 tag/提交的成功构建及 7 个完整附件，已发布成功时跳过重复构建。由工作流自身 `GITHUB_TOKEN` 创建的 Release 不会再次触发构建。

## 发布内容与验收

全量 Go 检查、前端构建与六个平台二进制构建通过后，上传 Linux amd64/arm64/armv7、Windows amd64、macOS amd64/arm64 包。HA 专项工作流作为可复用检查参与 tag 构建；Release 直接上传该检查生成的 `lalmax_nvr.zip`，避免重新打出未经检查的包。缺少任一预期产物会使发布失败。

包含 `-` 的 tag（例如 `v1.0.1-rc.1`）自动标记为预发布，其 Docker 镜像不会覆盖 `latest`、主版本或次版本稳定通道。Docker 版本会移除大小写 v/V 前缀，保证两种 tag 的镜像语义一致。

发布完成应核对 Actions 结果、7 个 Release 附件及二进制 `--version`，再用真实附件验证运行。HACS 使用本仓库 Release，首次安装和升级应从包含 HA 集成的 Release 验收，并确认配置、实体唯一标识和媒体功能保留。HACS 测试认证可预先配置，但不能把独立下载脚本成功记作界面安装成功。

Docker 推送后，`Released container smoke` 会在 amd64、arm64、ARMv7 上实际运行已发布二进制及内置 FFmpeg（按需使用 QEMU）。也可手动指定已有 tag 运行。运行检查使用仓库的包访问令牌，另用空 Docker 配置逐个架构验证匿名拉取。GHCR 新包默认私有，公开下载需组织管理员明确设置包可见性；改为公开后不能再改回私有。匿名拉取应另行验证。

## 重试

构建失败时优先修正问题并发布新 tag，保留已公开版本的身份。依赖或网络等暂时失败可以重跑失败任务。对已存在、且包含此工作流的 tag，也可从 Actions 手动运行 `Build` 并选择该 tag；手动选择分支只构建，不发布 Release。

参考：[GitHub Release 事件](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#release)、[GITHUB_TOKEN 触发限制](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow#triggering-a-workflow-from-a-workflow)。
