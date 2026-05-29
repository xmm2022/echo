# Echo

跨云盘资源池服务。把多家网盘（115、189pc、139 起步）的资源通过 `.echo` 占位文件统一管理：ingest 阶段在指定账号上秒传一份并落库，输出可被 Emby / OpenList / 媒体工具消费的 `.echo` 文件树；访问 `.echo` 时由 Echo 通过 sidecar 拿直链或代理流式回放，不再二次实例化。v0.1 中只有 115 支持"丢一个分享链接进来自动 ingest"（`115share2cas` 自动 exec）；139 走 manual import（用户自行跑 `cas139` 生成 `.cas` tree 后通过 `/api/ingest/manual` 导入）。

> 状态：v0.1.0 已发布（控制面 / DB / HTTP API / 管理后台 / Job runner / 监控与部署已落地）。设计文档见 [`docs/superpowers/specs/2026-05-28-echo-design.md`](docs/superpowers/specs/2026-05-28-echo-design.md)。

## 定位

Echo 是 NextEmby（闭源 PyArmor）和 RoseHub（闭源 Docker）之外的**第三个选择**：

- 完全开源（AGPLv3）
- 多云盘原生（115 / 189pc / 139，未来更多）
- 单 Go 二进制部署

## 快速开始

容器部署（推荐）:

```bash
cp .env.example .env          # 填入 ECHO_ADMIN_TOKEN / ECHO_SIDECAR_TOKEN
make docker                   # 构建本地 echo:dev 镜像 (docker/Dockerfile)
docker compose -f docker-compose.example.yml up
```

启动后:

- `GET /healthz` 永远 200（liveness）
- `GET /readyz` 在 sidecar token 未配 / 版本不符时返回 503；配齐后返回 200（DB ping + sidecar Ping + 版本校验）
- `GET /metrics` 暴露 Prometheus 指标（`echo_*` 业务指标 + Go/进程指标）
- 浏览器打开 `/`，粘贴 admin token 即可访问只读仪表盘

镜像里打包了 `115share2cas` 生产器（固定到 `docker/Dockerfile` 的 `SIDECAR_TOOLS_REF`）；`cas139`（Python）不打包，需在 139 客户端环境自行运行后走 manual import。反代示例见 [`nginx.example.conf`](nginx.example.conf)。

本地开发:

```bash
make build && make test
```

## 核心概念

- **Echo 文件 (`.echo`)**：极小的占位文件（几百字节），保存原始文件的名字、大小、多种 hash（md5 / sliceMd5 / sha1 / sha256 / preID）。ingest 阶段就已在指定账号下生成 live copy 并落库；访问 `.echo` 时 Echo 通过 sidecar 拿直链（`/api/restore/{file_id}`）或代理流式回放（`/api/stream/{file_id}`），**不重复实例化**。
- **跨云去重**：同一个文件的 hash 一旦在某个云盘出现，Echo 会记录其副本；其他云盘后续出现同一 hash 时自动关联到同一逻辑文件。
- **多账号资源池**：单个 provider 下可挂多个账号，秒传/恢复时按策略选账号。

## 生态位

```
Echo (本项目, AGPL) — 资源池控制面（业务逻辑 / DB / Web）
  ↓ HTTP REST 调用 (sidecar 进程边界,不通过 Go module import)
openlist-guangyapan-src (AGPL, xmm2022) — 多云 CAS sidecar (执行面: driver / CAS restore / Link / Stream)
  ↑ 派生
OpenList-CAS (AGPL, GitYuA) — 189pc CAS 起源
  ↑ 派生
OpenList (AGPL, OpenListTeam) — 通用网盘挂载框架
```

Echo 不重写 driver，也不通过 Go module import sidecar；而是站在 sidecar (`openlist-guangyapan-src`) 的 OpenList HTTP API 之上做业务编排（ingest / restore / 资源池 / Job），sidecar 进程边界即接口契约。

## HTTP API 与管理后台

`echo serve` 暴露一组 JSON API（`/api/accounts`、`/api/libraries`、`/api/ingest/{manual,producer}`、`/api/jobs`、`/api/libraries/{id}/entries`、`/api/conflicts`，以及 `/api/restore/{file_id}`、`/api/stream/{file_id}`）。v0.1 用单一静态 admin token（`auth.admin_token`）做 Bearer 鉴权，所有 API 与管理后台数据接口都在鉴权之后；`/healthz`、`/readyz`、`/metrics` 与数据无关的仪表盘外壳 `/` 公开。

最小管理后台（templ + htmx）在 `/`，只读地看 Job 与 hash 冲突。前端依赖 **vendored** 的 htmx（`internal/web/static/htmx.min.js`，不走 CDN，版本与来源见 [`internal/web/static/README.md`](internal/web/static/README.md)）。浏览器打开 `/` 后在页面里粘贴 admin token，脚本会把它作为 Bearer 头附加到后续 htmx 请求。

## 致谢

Echo 站在 OpenList 生态之上，CAS（秒传占位）模式与多云 driver 均来自上游，谨此致谢（全部 AGPLv3）:

- [OpenList](https://github.com/OpenListTeam/OpenList) — 通用网盘挂载与驱动框架
- [OpenList-CAS](https://github.com/GitYuA/OpenList-CAS) — 首创 `.cas` 占位文件 + 189pc 秒传恢复模式（CAS 模式起源）
- [openlist-guangyapan-src](https://github.com/xmm2022/openlist-guangyapan-src) — 把 CAS 扩展到 115 / 139 / 光鸭网盘的多云 sidecar（driver / CAS restore / Link / Stream 执行面）
  - `cmd/115share2cas`（`feat/cas-tools` 分支）— 115 分享 → 本地 CAS tree 生产器，已编译进 Echo 镜像
  - `tools/cas139`（`feat/cas-tools` 分支）— 139 分享 → CAS tree 生产器（Python，需在 139 客户端环境运行，产物走 manual import，**不**打包进镜像）

`castree/payload.go` 的字段表对齐自 `feat/cas-tools` 的 `pkg/casmeta`。各产生器版本固定到 Echo release notes 与 `sidecar.default.min_version`（详见 `docker/Dockerfile` 的 `SIDECAR_TOOLS_REF`），保证 release 可复现。

## License

AGPLv3。详见 [`LICENSE`](LICENSE)。

依据 AGPLv3 §13：网络交互本身**不**等同于 GPL 意义上的 convey / distribution，部署未经修改的 Echo 不触发 §13 的额外源码提供义务（但仍受 §4–§6 约束：保留版权声明、衍生作品继续 AGPLv3 等）。若部署的是经过修改的 Echo 版本并对远程用户提供网络服务，则必须向这些用户提供该修改版的 Corresponding Source。完整解读见设计文档 §10。
