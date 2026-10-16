# Render 特别版更新记录 · 2026-10-08

本次以用户提供的 `opencode2api-main` 原项目代码快照为上游，同步到 Render 特别版。两个目录均不包含 Git 历史，无法据此确认发布标签或提交号；程序继续使用上游的版本注入方式。

## 同步内容

- Go 共享套餐额度看板，以及本地 API Key 的创建、命名、启停、删除和即时生效。
- `sota` / `sweet` 模型分组轮转、状态持久化、首个有效输出保护和流式错误处理。
- 免费模型可用性探测、手动恢复、协议信息展示和经过探测验证的自适应思考强度。
- 首个 SSE 事件等待超时、模型元数据来源及上下文限制补充、上游诊断日志和 System One 请求校验等上游改动。
- 新版 WebUI、配置示例、管理说明、许可证、Docker 构建文件和开发检查配置。GitHub 的检查与发布验证流程均加入 `go test ./...`。

免费模型探测沿用上游的后台行为，会定期访问已配置的免费通道。详细规则及配置说明见 [README](../README.zh-CN.md) 和[管理控制台说明](management.zh-CN.md)。

## 保留的 Render 适配

- 自动读取 `PORT` 并监听 `0.0.0.0:$PORT`；API 与 WebUI 共用公网端口。`-single-port` 和本地双端口模式继续可用。
- `/v1/*` 进入 API 网关，根路径及 `/api/*` 进入 WebUI；新管理接口沿用登录会话和 CSRF 校验。
- `/livez` 仅检查进程存活，即使模型尚未就绪也返回 200；`/healthz` 继续报告就绪状态。两种检查均不计入请求统计。
- `render.yaml`、Secret File 的 GID 1000 访问适配、首次导入配置、缺失种子配置时明确退出，以及 `umask 077` 保持原行为。
- Docker 健康检查使用 `PORT` 对应端口的 `/livez`；镜像同时包含上游新增的许可证文件和声明。

## 更新现有服务

将本目录内容更新到现有 Render 服务使用的仓库并重新部署，继续使用原 Secret File、环境变量及持久磁盘设置。Dockerfile 与 `render.yaml` 所在目录应为部署根目录。

旧配置可直接加载，新增字段会使用默认值。已有本地 Key、上游 Key 和 WebUI 账号保留；管理界面的明文初始密码仍按原流程迁移为哈希。新增 API Key 元数据保存在 `config.json`，轮转状态保存在 `config.json.rotation.json`，模型可用性状态保存在 `config.json.<上游地址指纹>.availability.json`。

持久磁盘应继续挂载到 `/var/lib/opencode2api`。没有持久磁盘时，实例重建会丢失在 WebUI 中修改的配置及新增状态，并重新从 Secret File 初始化。已有持久配置时，修改 Secret File 不会覆盖当前配置。

升级前备份当前线上配置和状态；需要回退时同时恢复旧程序及与其匹配的配置。旧版程序不认识新增的配置字段。

## 本轮本地验证

环境：Windows amd64，Go 1.24.13，Node.js 24.18.0，Prettier 3.9.6。

已执行并通过：

- `go test -count=1 -v ./...`：端口输入、API / WebUI 路由、模型未就绪时的存活检查及统计隔离。
- 使用本地模拟上游验证管理登录、Render HTTPS 转发下的安全 Cookie、CSRF、额度读取及缓存、模型别名和 System One 能力发现、API Key 创建/启停/删除即时生效、最后一个 Key 保护、配置迁移和状态文件保存。
- 使用本地模拟 SSE 上游验证轮转别名解析、单端口流式输出、在上游完成前交付有效内容，以及别名和结束标记保留。
- `go vet ./...`、Go 格式检查、`npm run format:check`、`npm run check:web`、`sh -n docker-entrypoint.sh`。
- Windows amd64 构建，以及使用 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` 的 Linux 构建；构建入口为 `./cmd/opencode2api`。
- 原项目 79 个文件的 SHA-256 与操作前记录一致。更新前 Render 版 58 个文件已独立备份并逐文件校验。

验证全部使用测试配置和本地模拟接口。本轮未进行真实 OpenCode 推理、Docker 镜像构建或 Render 线上部署测试；当前环境未提供 Docker 和可调用的 C 编译器，因此也未运行 `go test -race`。上游文档中保留的历史测试说明不代表本轮执行结果。
