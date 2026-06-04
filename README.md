# Echo

跨云盘资源池服务。把多家网盘（115、189pc、139 起步）的资源通过 `.echo` 占位文件统一管理：ingest 阶段在指定账号上秒传一份并落库，输出可被 Emby / OpenList / 媒体工具消费的 `.echo` 文件树；访问 `.echo` 时由 Echo 通过 sidecar 拿直链或代理流式回放，不再二次实例化。v0.1 中只有 115 支持"丢一个分享链接进来自动 ingest"（`115share2cas` 自动 exec）；139 走 manual import（用户自行跑 `cas139` 生成 `.cas` tree 后通过 `/api/ingest/manual` 导入）。

> 状态：v0.3 discovery 主线已实现并通过本地验证；正式 tag / release 元数据仍待最终真实环境 gate 和 sidecar 版本钉住。基础设计文档见 [`docs/superpowers/specs/2026-05-28-echo-design.md`](docs/superpowers/specs/2026-05-28-echo-design.md)，v0.3 设计见 [`docs/superpowers/specs/2026-06-02-echo-v0.3-design.md`](docs/superpowers/specs/2026-06-02-echo-v0.3-design.md)。

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

## CAS 生产：内置 producer vs 外部 cas-pipeline

Echo 自己**不生产** CAS（不抓分享、不算 hash），CAS payload 必须由生产器产出。两条路径，定位不同：

- **外部 `cas-pipeline`（可控主力）**：独立工具，用 115 webapi 编排「转存进自有账号 → 建永久分享 → 生成 CAS → 删临时」（stage 模式），产出 `.cas tree` + `manifest.jsonl`，再走 `POST /api/ingest/manual` 导入。这是建设 115 资源池的**可控主力**——资源落在你自己账号下，不依赖他人分享存活。stage 那套有状态 / 有副作用的编排（配额、回收站、审核轮询）**不进 Echo**。
- **内置 `115share2cas` producer（机会主义）**：`POST /api/ingest/producer` 直接 exec `115share2cas` 的 direct 模式，从**他人的分享链接**直接抽 CAS。方便，但**依赖该分享存活、不可控**，仅作机会主义入口。

> 边界原则：Echo = CAS 消费 + 资源池（manual import + restore/stream + 跨云去重）；CAS 生产（尤其 115 的 stage 编排）交给外部工具。两者通过 `.cas tree` + `manifest.jsonl` 文件解耦，符合"进程边界即接口契约"的同源思路。

115 运维细节补充见 [`docs/superpowers/release-gates/2026-06-03-echo-115-cas-behavior.md`](docs/superpowers/release-gates/2026-06-03-echo-115-cas-behavior.md)：它区分了 live copy、普通 115 `.cas`、以及带 `source.type=115_share` 的 share-backed `.cas`，避免把"可播放资产"和"可重导入资产"混为一谈。

## HTTP API 与管理后台

`echo serve` 暴露一组 JSON API（`/api/accounts`、`/api/libraries`、`/api/ingest/{manual,producer}`、`/api/jobs`、`/api/libraries/{id}/entries`、`/api/conflicts`，以及 `/api/restore/{file_id}`、`/api/stream/{file_id}`）。v0.1 用单一静态 admin token（`auth.admin_token`）做 Bearer 鉴权，所有 API 与管理后台数据接口都在鉴权之后；`/healthz`、`/readyz`、`/metrics` 与数据无关的仪表盘外壳 `/` 公开。

最小管理后台（templ + htmx）在 `/`，只读地看 Job 与 hash 冲突。前端依赖 **vendored** 的 htmx（`internal/web/static/htmx.min.js`，不走 CDN，版本与来源见 [`internal/web/static/README.md`](internal/web/static/README.md)）。浏览器打开 `/` 后在页面里粘贴 admin token，脚本会把它作为 Bearer 头附加到后续 htmx 请求。

### v0.2 Emby 反向代理（`/emby`）

启用 `emby_proxy` 后，Echo 在 `proxy_prefix`（默认 `/emby`）下挂一层 Emby 反向代理：

- Emby 客户端把服务器地址填成 `https://echo.example.com/emby`（本地测试用 `http://localhost:8080/emby`），其余请求透传到上游 Emby。
- 命中库映射的 PlaybackInfo 源会被改写成 `/emby/stream/{token}` 或 `/emby/error/{token}`，上游 Emby 的真实 stream URL、签名 URL 与鉴权头一律不下发给客户端。
- `/api/restore/{file_id}` 与 `/api/stream/{file_id}` 仍是 Echo 的管理 / v0.1 兼容 API，**不是** Emby PlaybackInfo 的改写目标，不要把它们填进 Emby。
- `auth.bootstrap_admin_token` 只用于找回 / 签发 admin token；日常管理请求走 DB 里的 API token，而非这个 bootstrap 凭据。
- 上游 Emby API key 通过 `emby_proxy.upstream.api_key_ref` 引用，支持 `env:NAME`（如 `env:EMBY_API_KEY`）或 `ref:relative/path`（相对 `secrets_root` 的常规文件，禁止绝对路径 / `..` / 软链逃逸）。

### v0.3 Discovery 自动订阅

启用 `discovery` 后，Echo 增加一层 admin-only 自动订阅管理面：

- TMDB 订阅缓存和搜索：`/api/discovery/tmdb/search`。
- Source 管理：Telegram MTProto source、poster HTTP source 和 manual source。
- 规则评分：rule profile 解析标题 / 分辨率 / HDR / 音轨 / 体积 / 扩展名等特征，生成可复现 score snapshot。
- 候选与 match 决策：`/api/discovery/candidates`、`/api/discovery/matches`，支持 accept / reject / retry。
- 115-only dispatch：accepted 115 分享会排成现有 `ingest_producer` job，仍由 `115share2cas` + v0.1 ingest 写入 `.echo`。
- 管理后台首页已挂载 discovery subscriptions、sources、producer profiles、rule profiles、candidates、matches 和 runs 面板。

Discovery 不直接写 `library_entries` / `file_copies`，也不直接 import sidecar Go 包；它只负责发现、评分、决策和把 115 分享交给现有 producer pipeline。当前 release 不强制 Telegram 登录；Telegram MTProto 真机 gate 是可选 operator gate，环境变量见 [`docs/superpowers/release-gates/2026-06-02-echo-v0.3-discovery.md`](docs/superpowers/release-gates/2026-06-02-echo-v0.3-discovery.md)。

## 致谢

Echo 站在 OpenList 生态之上，CAS（秒传占位）模式与多云 driver 均来自上游，谨此致谢（全部 AGPLv3）:

- [OpenList](https://github.com/OpenListTeam/OpenList) — 通用网盘挂载与驱动框架
- [OpenList-CAS](https://github.com/GitYuA/OpenList-CAS) — 首创 `.cas` 占位文件 + 189pc 秒传恢复模式（CAS 模式起源）
- [openlist-guangyapan-src](https://github.com/xmm2022/openlist-guangyapan-src) — 把 CAS 扩展到 115 / 139 / 光鸭网盘的多云 sidecar（driver / CAS restore / Link / Stream 执行面）
  - `cmd/115share2cas`（`feat/cas-tools` 分支）— 115 分享 → 本地 CAS tree 生产器，已编译进 Echo 镜像
  - `tools/cas139`（`feat/cas-tools` 分支）— 139 分享 → CAS tree 生产器（Python，需在 139 客户端环境运行，产物走 manual import，**不**打包进镜像）

`castree/payload.go` 的字段表对齐自 `internal/casmeta`（cas-tools baseline commit `3324d78`，release producer ref `814736c203e2115bb2dfda597f625c676a5cda74` / tag `echo-115cas-hotfix-20260604`；9 字段含 `provider/sha1/preID`，`provider=115` 时 `sha1`+`preID` 必填）。各产生器版本固定到 Echo release notes 与 `sidecar.default.min_version` 决策（详见 `docker/Dockerfile` 的 `SIDECAR_TOOLS_REF`），保证 release 可复现。

## License

AGPLv3。详见 [`LICENSE`](LICENSE)。

依据 AGPLv3 §13：网络交互本身**不**等同于 GPL 意义上的 convey / distribution，部署未经修改的 Echo 不触发 §13 的额外源码提供义务（但仍受 §4–§6 约束：保留版权声明、衍生作品继续 AGPLv3 等）。若部署的是经过修改的 Echo 版本并对远程用户提供网络服务，则必须向这些用户提供该修改版的 Corresponding Source。完整解读见设计文档 §10。
