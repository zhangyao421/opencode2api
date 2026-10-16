# Render 版同步说明（2026-10-16）

本次以 `E:\AI\opencode\opencode2api-main` 为功能基线，更新 `opencode2api-render`，并保留 Render Web Service 的单端口部署适配。

## 已同步内容

- 同步原仓库最新的匿名推理地址、OpenCode 2.x User-Agent 和会话身份格式。
- 同步每次上游尝试超时、客户端取消和代理不可用错误处理，以及真实 OpenCode 工具定义和图片工具结果转换。
- 同步免费模型 `free_only`、手动/自动可用性控制、探测状态迁移、成本与缓存价格元数据、推理强度元数据。
- 同步按本地 API Key、会话和 Playground 分类的用量统计，以及管理界面的 API Key 用量展示。
- 同步协议转换、Responses reasoning、工具结果、模型目录和前端管理页面的上游修复。

## 保留的 Render 适配

- 读取 Render 注入的 `PORT`，监听 `0.0.0.0:$PORT`，将 API 和 WebUI 合并到一个公网端口。
- 保留 `/livez` 存活检查；它不依赖模型目录或上游可用性，也不计入请求统计。`/healthz` 继续报告就绪状态。
- 保留 `render.yaml`、Secret File 的 GID 1000 访问、`CONFIG_SEED_PATH` 首次导入、缺失种子配置时明确退出和严格文件权限。
- Docker 健康检查使用 `PORT` 对应的 `/livez`，本地未设置 `PORT` 时仍可使用 API `8080` 和 WebUI `8081`。

## 本轮验证

- 修复 Render 启动失败：`docker-entrypoint.sh` 已转换为 LF 换行，避免 Linux 将 shebang 解析为 `/bin/sh\r` 并报 `no such file or directory`；`.gitattributes` 已统一约束所有 `.sh` 文件使用 LF。
- `go test ./...`：通过，包含端口、单端口路由、存活检查和 Render 管理/流式集成测试。
- `go vet ./...`：通过。
- `gofmt`：通过，26 个同步或适配的 Go 文件。
- Windows amd64 和 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` 构建：通过。
- `node --check webui/app.js`：通过。
- `sh -n docker-entrypoint.sh`：通过。
- JSON 配置文件解析：通过。

验证使用本地模拟接口和 Go 1.24.9 工具链；未执行真实 OpenCode 推理、Docker 镜像构建或 Render 线上部署。临时工具链、构建产物、差异文件和校验记录已移动到 `E:\AI\待删除文件\opencode2api-render-validation-20261016`，由用户最终清理。
