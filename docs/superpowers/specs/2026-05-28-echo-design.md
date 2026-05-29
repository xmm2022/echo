# Echo v0.1 设计

- 日期: 2026-05-28
- 作者: Echo 项目
- 状态: 设计稿,待评审

## 1. 项目定位

Echo 是一个跨云盘资源池**控制面**服务。云盘**执行面**通过 HTTP 委托给 OpenList sidecar (基于 `openlist-guangyapan-src`),Echo 自身只跑业务逻辑 + DB + Web/API,一份单二进制。

一句话: **消费由 producer 工具 (`115share2cas` 自动 exec / `cas139` 用户手工跑后 manual import / 用户已有 .cas tree) 产出的 CAS 树,经 sidecar 在目标账号上恢复出 live copy,落库后输出 `.echo` 占位文件树,供 Emby / 媒体工具消费;访问时由 Echo 通过 sidecar 拿直链或代理流式回放。**

### 灵感来源与差异化

| 项目 | 形态 | 云盘范围 | 存储 |
|---|---|---|---|
| RoseHub | 闭源 Docker | 115 单云 | STRM 指向真实云盘文件 |
| NextEmby | 闭源 Python | 115 单云(p115client) | 用户账号秒传转存 |
| **Echo** | **开源 Go (AGPLv3)** | **多云: 115 + 189pc + 139 (光鸭网盘不承诺)** | **`.echo` 占位 + sidecar CAS restore** |

Echo 给现有生态加上"第三个选择": 完全开源, 多云原生, 控制面/执行面分离。

### 控制面 / 执行面 框定

```
+---------------------+      HTTP       +---------------------------+
|   Echo (控制面)      | <-------------> |  OpenList sidecar (执行面) |
|   - 业务逻辑          |  REST + 直链    |  - 多云 driver            |
|   - DB / Job / Web   |                 |  - CAS restore            |
|   - Restore proxy    |                 |  - Link / Stream          |
+---------------------+                 +---------------------------+
        ^                                         ^
        | manifest + .cas tree                    | (Echo 不重写,不 import)
        |                                         |
+------------------------------+         +-------------------------+
|  Producer (一次性)            |         | openlist-guangyapan-src |
|  - 115share2cas (Go CLI)     |         |  GitHub:                |
|  - cas139 (Python, 用户手工) |         |  xmm2022/openlist-...   |
|  - 用户已有 .cas tree         |         |  branch: feat/cas-tools |
+------------------------------+         +-------------------------+
```

**控制面 (Echo)**: 资源池业务层,管理 library / blob / copy / job,负责对外提供 Ingest / Restore / Manifest API + Web 后台。

**执行面 (OpenList sidecar)**: 多云 driver 实现 + CAS restore 模式。继续在 `github.com/xmm2022/openlist-guangyapan-src` 仓库迭代,Echo 通过 OpenList 的 REST API 调用它,**不 import 任何 Go 包**。

**Producer**: 一次性 CAS 树产出工具,直接来自 `openlist-guangyapan-src` 的 `feat/cas-tools` 分支 (`cmd/115share2cas/`, `tools/cas139/`)。Echo 不重新实现,只消费产物。v0.1 仅 `115share2cas` 在 Echo 内自动 exec;`cas139` 落 139 云端,用户手工跑后下载本地走 manual import。

Echo 自身实现的是: ingest pipeline (消费 .cas tree) / 跨云资源池数据库 / job 调度 / restore API / manifest API / web 后台 / sidecar HTTP client。

### Provider matrix (v0.1)

| Provider | Ingest 方式 | Restore 方式 | 备注 |
|---|---|---|---|
| 115 | `115share2cas` 产出 CAS tree (含 sha1 + preID) | sidecar rapid-only,provider=115 + sha1 + preID | 115 额外区间校验可能失败,失败即 item failed,不 fallback |
| 139 | `cas139` 工具产出 CAS tree (用户自行跑,落 139 云端 → 下载到本地 → manual import;v0.1 **不**在 Echo 内 auto-exec) | sidecar 在 `personal_new` 模式 + 启用 CAS restore | files-JSON 需预先抓取 |
| 189pc | 只消费已有 .cas tree / sidecar 自生成 CAS (**不**写"已挂载路径泛化") | sidecar 用 md5 + sliceMd5 | 来自上游 OpenList-CAS |
| 光鸭网盘 | 不承诺 | 不承诺 | sidecar 已有 driver,Echo 不主动支持 |

### 生态位与依赖关系

```
Echo (本项目, AGPLv3) ── 资源池控制面
  │ HTTP (REST) 调用
  v
openlist-guangyapan-src (AGPLv3, xmm2022) ── 多云 CAS sidecar
                                              + 多云 driver
                                              + feat/cas-tools 上的
                                                115share2cas / cas139
  ↑ 派生
OpenList-CAS (AGPLv3, GitYuA) ── 189pc CAS 模式起源
  ↑ 派生
OpenList (AGPLv3, OpenListTeam) ── 通用网盘挂载框架
```

Echo 与 sidecar 是**进程级解耦**,版本升级互不强约束 (有 API 兼容矩阵)。

### v0.1 范围

包含:
- Ingest pipeline: 消费 producer 产出的 CAS tree (manifest + .cas 文件),sidecar restore-only,生成 `.echo` 占位树
- Producer job: Echo 可以 exec 调 `115share2cas`,等退出后消费 manifest + CAS tree (限制工作目录 + 参数白名单,防 shell 注入)。**v0.1 不在 producer-exec 路径支持 cas139** (它产物落 139 云端,无本地 manifest);139 走 manual import 入口。
- Manual import: 用户已有 CAS tree,Echo 直接导入
- 跨云资源数据库 (blobs / library_entries / file_copies / blob_hashes / hash_conflicts)
- 多账号管理 (每个 provider 多账号,凭据放在 sidecar 上,Echo 只引用 account_id)
- Restore API: 双 endpoint
  - `GET /api/restore/{file_id}` → JSON `{url, headers, expires_at}` (给客户端自己访问)
  - `GET /api/stream/{file_id}` → Echo 代理流式转发 (给 Emby / 普通客户端)
- Manifest API: 列出库内 `.echo` 树及多云副本状态
- 最小 Web 后台 (账号 / 库 / Job / 冲突告警)
- Auth middleware (v0.1 静态 ADMIN_TOKEN)
- 健康检查 + Prometheus metrics

不包含:
- 用户系统、注册、登录页 (预留 `owner_id` 列 + auth 中间件占位;v0.2 替换)
- Emby 反向代理 / `PlaybackInfo` 改写 (v0.2)
- TG 爬虫 / TMDB 订阅 / 海报墙 (v0.3)
- 跨 hash conflict 自动 merge (只记 `hash_conflicts` 表 + admin 告警,人工处理)
- 真实文件上传 (Echo 是 CAS-only 服务,所有数据走 sidecar restore;**任何 fallback 到真实上传都被显式拒绝**)
- 在 Echo 内重新实现任何 driver / share 解析 / 抓 cookie (这些全是 sidecar / producer 的职责)

### v0.1 之上路线

- **v0.2**: Rose-like Emby 播放代理 (消费 Echo manifest + 多用户 Cookie 池)
- **v0.3**: NextFind-like 自动订阅 (用 Ingest API + TMDB + TG 抓取)

## 2. 架构与模块

### 部署形态 (双进程: Echo + sidecar)

```
+-------------------------------------------+        +-------------------------------------+
| echo (Go binary, :8080)                   |        | OpenList sidecar (:5244)            |
|                                            |        |                                     |
|   HTTP API + Web UI                        |        |   多云 driver:                       |
|   Job runner (goroutine pool)              | <----> |     115 / 139 / 189pc / 光鸭         |
|   SQLite at /data/echo.db                  |  REST  |   CAS restore (rapid-only)          |
|   .echo output: 本地 fs                    |        |   Link / Stream                     |
|                                            |        |   Account & token 生命周期          |
|   +-----------------------------------+    |        |                                     |
|   | sidecarclient (HTTP REST client)  |    |        |   存储挂载在 sidecar config         |
|   |   ListStorages / PutCAS            |    |        |   (含 cas_restore=true 开关)        |
|   |   Link / Stream                   |    |        |                                     |
|   +-----------------------------------+    |        +-------------------------------------+
+-------------------------------------------+
                  ^
                  | (一次性 exec,非常驻)
                  v
        +-----------------------------+
        | Producer 工具                |
        |   115share2cas (Go, auto)   |
        |   cas139 (Python, 手工)      |
        +-----------------------------+
```

部署边界:
- **同主机**: Echo 与 sidecar 同 docker-compose,通过 service 名互通 (`http://sidecar:5244`)
- **跨主机** (v0.2+): sidecar 独立部署,Echo 通过反代 + 认证 token 访问
- **多 sidecar**: 一个 Echo 实例可以挂多个 sidecar (后期路由,v0.1 假设单 sidecar)

### Echo 与 sidecar 的契约

Echo 调 sidecar 的 OpenList HTTP API,具体调用面 (v0.1):

| 用途 | sidecar endpoint | 备注 |
|---|---|---|
| 健康检查 | `GET /ping` | readyz 时探测 |
| 账号 / Storage 列表 | `GET /api/admin/storage/list` | 供 Echo 把 account 列出来给用户绑定 |
| 列分享内容 | provider 自带的 share API (driver 内部封装) | 仅用作 manual debug;ingest 主路径不依赖 |
| CAS restore | `PUT /api/fs/put` 单文件上传 `*.cas` (driver Put 内识别 .cas 后走 rapid-only restore) | v0.1 主路径,逐文件 PUT,无批量 endpoint |
| 取直链 | `POST /api/fs/link` (AuthAdmin) | 供 Echo 拿 url + headers |
| 流式代理 | `GET /d/<path>` (或对应签名链接) | Echo 反向代理给客户端 |

Echo **不**调以下东西 (越界):
- sidecar 内部数据库 / config 文件
- driver Go 类型 / interface
- internal/op / internal/model

凭据存储在 sidecar 上 (sidecar 自己负责加密 + 刷新)。Echo DB 里 `accounts` 表只存 sidecar 上 storage 的引用 (sidecar id + storage mount path),不复制凭据。

### Echo 包结构

```
echo/
├── cmd/echo/main.go                    入口
├── internal/
│   ├── config/                         env + yaml 加载
│   ├── http/
│   │   ├── server.go
│   │   ├── middleware/auth.go          v0.1 静态 ADMIN_TOKEN
│   │   └── handlers/
│   │       ├── ingest.go               POST /api/ingest/manual + POST /api/ingest/producer
│   │       ├── jobs.go
│   │       ├── manifest.go
│   │       ├── restore.go              GET /api/restore/{file_id} (JSON)
│   │       ├── stream.go               GET /api/stream/{file_id} (proxy)
│   │       ├── accounts.go             绑定 sidecar storage
│   │       ├── conflicts.go            列出 hash_conflicts
│   │       └── library.go
│   ├── store/                          数据访问层 (sqlc)
│   │   ├── schema/                     *.up.sql / *.down.sql (golang-migrate)
│   │   ├── blobs.go
│   │   ├── library_entries.go
│   │   ├── copies.go
│   │   ├── blob_hashes.go
│   │   ├── hash_conflicts.go
│   │   ├── jobs.go
│   │   ├── accounts.go
│   │   └── libraries.go
│   ├── sidecarclient/                  HTTP client to OpenList sidecar
│   │   ├── client.go                   认证 + 重试 + 超时
│   │   ├── storage.go                  account/storage 列表
│   │   ├── putcas.go                   PUT /api/fs/put 单文件 .cas (rapid-only restore)
│   │   ├── link.go                     POST /api/fs/link
│   │   └── stream.go                   /d/<path> 反向代理
│   ├── castree/                        CAS tree + manifest 消费
│   │   ├── reader.go                   遍历目录,读 .cas
│   │   ├── manifest.go                 manifest.jsonl 解析
│   │   └── payload.go                  base64 JSON 解码 (Echo 自实现,字段表与 cas-tools 对齐)
│   ├── echofile/                       .echo 文件输出
│   │   └── output.go                   写到 local fs
│   ├── ingest/
│   │   ├── pipeline.go                 consume cas tree → sidecar restore → write .echo
│   │   ├── dedup.go                    job item dedup key
│   │   └── producer.go                 exec 115share2cas / cas139,等退出后消费
│   ├── restore/
│   │   ├── resolver.go                 file_id → live copy → sidecar link
│   │   ├── proxy.go                    /api/stream/{file_id} 流式转发
│   │   └── cache.go                    内存直链缓存 TTL=60s
│   ├── job/
│   │   ├── runner.go                   goroutine pool + 取消
│   │   ├── progress.go
│   │   └── persistence.go
│   └── web/
│       ├── templates/                  htmx + templ
│       └── static/
├── go.mod
├── go.sum
├── docker/Dockerfile                   multi-stage, scratch base
├── docker-compose.example.yml          示例: echo + sidecar 双服务
├── scripts/
│   ├── dev.sh
│   └── migrate.sh
├── docs/
│   └── superpowers/specs/              本文档
├── integration/                        build tag = integration
└── testdata/                           manifest / .cas tree fixtures
```

### 为什么不 Go module import sidecar

之前评估过把 `openlist-guangyapan-src` 作为 Go module 直接 import,**已否决**:

1. `openlist-guangyapan-src/internal/*` 在 Go module 规则下不可被外部 import,driver 实现几乎全部在 internal
2. driver 接口暴露大量 `internal/model.*` 类型 (`FileStreamer`、`Storage`、`Driver`),把这些 type 提到 pkg 会侵入上游主干
3. driver 在 sidecar 内嵌进 `internal/op` 的 storage manager,旁路这个 manager 需要重写一份 lifecycle 适配,跟 sidecar 主进程长期分叉,维护成本高
4. CAS restore 已经是 sidecar 内部成熟路径 (`cas_restore.go`),作为 HTTP 调用更稳定

所以 Echo 与 sidecar 的所有交互通过 OpenList REST API,以 sidecar 进程边界为接口契约。

### sidecar 版本兼容

Echo 在 `sidecarclient` 里写一个 **最低 sidecar 版本声明** (e.g. `>= openlist-guangyapan-src@feat/cas-tools` 后某 commit)。启动时调 `GET /ping` 或 `GET /api/public/settings` 拿 sidecar 版本,版本不匹配则 readyz 失败 + 日志告警。具体版本号在 Echo release notes 里固定。

### 核心 interface (Echo 内部)

```go
// sidecar client: 业务层只依赖这个,不感知具体 provider
type Sidecar interface {
    Ping(ctx context.Context) error
    Version(ctx context.Context) (string, error)

    ListStorages(ctx context.Context) ([]Storage, error)

    // Ingest 路径: 把一个 .cas 文件 PUT 到目标 storage,
    // sidecar 内 driver.Put 识别 .cas 扩展名后走 rapid-only restore,
    // 返回该文件的恢复结果 (sidecar 上无批量入口,Echo 负责遍历 .cas tree 逐文件调用)
    PutCAS(ctx context.Context, req PutCASRequest) (*ItemResult, error)

    // Restore 路径
    Link(ctx context.Context, storageMount, remotePath string) (*DirectLink, error)
    Stream(ctx context.Context, req StreamRequest) (*StreamResult, error)
}

type PutCASRequest struct {
    StorageMount string    // sidecar 上 storage 挂载点 (e.g. "/115-main")
    RemoteDir    string    // storage 内目标目录 (绝对路径,Echo 已 join target_subdir + rel_path 的 parent)
    CASName      string    // 上传时的 .cas 文件名 (e.g. "Movie.S01E01.mkv.cas",driver Put 后落为 "Movie.S01E01.mkv")
    CASBody      io.Reader // .cas payload 字节流
    CASSize      int64     // Content-Length
}

type ItemResult struct {
    Status     string                // restored / skipped_dup / failed
    Error      string                // 失败原因 (provider-specific code)
    CloudPath  string                // 在 storage 上的最终路径 (.cas 去后缀后的真实文件名)
    SizeBytes  int64
    Hashes     map[string]string     // sidecar 实际确认的 hash 集 (可空,driver Put 不保证全部回传)
}

// Stream: 媒体客户端透传式代理 (Range / If-Range / If-Modified-Since 必须传给上游;
// sidecar 应回 200 / 206 / 304 与对应的 Content-Length / Content-Range / Accept-Ranges 等头)
type StreamRequest struct {
    StorageMount string
    RemotePath   string
    Headers      http.Header   // 客户端原始请求头中需要透传的子集 (Range / If-Range / If-Modified-Since / If-None-Match / User-Agent)
}

type StreamResult struct {
    StatusCode int               // 200 / 206 / 304 / 416 等,handler 透写到 ResponseWriter
    Header     http.Header       // Content-Length / Content-Range / Content-Type / Accept-Ranges / Last-Modified / ETag
    Body       io.ReadCloser     // 304 时为 nil
}

// Producer: ingest 输入抽象 (本地 CAS tree)
type ProducerJob interface {
    // exec 一个 producer (e.g. 115share2cas),返回 manifest 路径与 cas tree 路径
    Run(ctx context.Context) (manifestPath, casTreePath string, err error)
}
```

`.echo` payload 与 sidecar `feat/cas-tools` 输出的 `.cas` 字段表完全对齐,Echo 在 `castree/payload.go` 自实现 encode/decode,**不**依赖 sidecar Go 包。这是字段表层面的兼容,不是代码层面的复用。

## 3. 数据模型

### 三层拆分: blob / library_entries / file_copies

Echo 不再把"文件"塞成单表,而是拆成三层:

- `blobs` — 一个**物理文件**的指纹与体积 (跨 cloud 的 dedup 锚点)
- `library_entries` — 一个 library 内的**逻辑路径**指向某个 blob (库内目录树)
- `file_copies` — 某个 blob 在某个 sidecar/storage/account 上的**实际副本** (live copy 元数据)

这种拆分让以下情形天然成立:
- 同一 blob 在多个 library 里以不同路径出现 (合集 / 链接)
- 同一 blob 在多云上有多个副本 (115 + 189pc + 139)
- 跨 hash 发现是同一个 blob 时,只用 merge blob,不动 library 树

`blob_hashes` 是辅助索引,允许一个 blob 同时记录 sha1 / md5 / sha256 / preid / slice_md5。`hash_conflicts` 在跨 hash 发现矛盾时不阻塞 ingest,而是记录人工审阅。

### DDL (SQLite, v0.1)

v0.1 **只支持 SQLite**;PostgreSQL 不在承诺范围内。下方 DDL 用 SQLite 语法 (`INTEGER PRIMARY KEY AUTOINCREMENT` 等)。如果将来要做 PG migration,需要单独设计 schema (`SERIAL` / `BIGSERIAL` 替主键、boolean 列独立、索引名按 PG 重命名),不沿用本节 SQL。

```sql
-- 账号引用 (实际凭据存在 sidecar)
CREATE TABLE accounts (
  id              TEXT PRIMARY KEY,                  -- 用户起的名字: "115-main"
  provider        TEXT NOT NULL,                     -- 115 / 139 / 189pc / guangyapan
  sidecar_id      TEXT NOT NULL,                     -- 多 sidecar 时区分 (v0.1 默认 "default")
  storage_mount   TEXT NOT NULL,                     -- sidecar 上 storage 挂载点 (例如 "/115-main")
  status          TEXT NOT NULL,                     -- ok / token_expired / banned / unknown
  last_check      INTEGER,
  owner_id        TEXT NOT NULL DEFAULT 'admin',
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  UNIQUE (sidecar_id, storage_mount)
);

-- 资源库 (.echo 输出目录的语义包装)
CREATE TABLE libraries (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  name             TEXT NOT NULL,
  echo_output_kind TEXT NOT NULL,                    -- v0.1 仅支持 "local"; v0.2+ 加 "webdav" / "s3"
  echo_output_path TEXT NOT NULL,                    -- 本地路径,例如 /data/output/lib1
  owner_id         TEXT NOT NULL DEFAULT 'admin',
  created_at       INTEGER NOT NULL
);

-- Blob: 一个物理文件的跨云身份 (size + canonical hashes)
CREATE TABLE blobs (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  size            INTEGER NOT NULL,
  canonical_name  TEXT,                              -- 首次见到的文件名 (仅展示用)
  source_mtime    INTEGER,                           -- 首次见到的 mtime (仅展示用)
  owner_id        TEXT NOT NULL DEFAULT 'admin',
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);

-- Library 内的逻辑路径 (用户能看到的目录树)
CREATE TABLE library_entries (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  library_id    INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  rel_path      TEXT NOT NULL,                       -- 库内相对路径 (含目录与文件名)
  name          TEXT NOT NULL,                       -- 显示名 (rel_path 的 basename)
  blob_id       INTEGER NOT NULL REFERENCES blobs(id),
  echo_written  INTEGER NOT NULL DEFAULT 0,          -- 是否已写出 .echo 文件 (0/1)
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  UNIQUE (library_id, rel_path)
);
CREATE INDEX idx_library_entries_blob ON library_entries(blob_id);

-- Blob 的多 hash 索引 (跨 hash 查 + 跨 hash dedup)
CREATE TABLE blob_hashes (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  blob_id          INTEGER NOT NULL REFERENCES blobs(id) ON DELETE CASCADE,
  hash_type        TEXT NOT NULL,                    -- md5/sha1/sha256/preid/slice_md5
  hash_value       TEXT NOT NULL,                    -- 原始 (大小写敏感)
  hash_value_norm  TEXT NOT NULL,                    -- 规范化 (lower-case + 去分隔符)
  size             INTEGER NOT NULL,                 -- 由 app 层从 blob.size 复制写入
  UNIQUE (hash_type, hash_value_norm, size)
);
CREATE INDEX idx_blob_hashes_blob ON blob_hashes(blob_id);

-- 注: hash_value_norm + size 的复合 UNIQUE 是为了防止 hash 碰撞跨 size 的误匹配
-- 即使 sha1 极端碰撞,size 不同就不算同一 blob

-- 多云副本: blob 在某个 sidecar/storage/账号/路径 上的 live copy
CREATE TABLE file_copies (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  blob_id        INTEGER NOT NULL REFERENCES blobs(id) ON DELETE CASCADE,
  provider       TEXT NOT NULL,
  account_id     TEXT NOT NULL REFERENCES accounts(id),
  sidecar_id     TEXT NOT NULL,
  storage_mount  TEXT NOT NULL,
  remote_path    TEXT NOT NULL,                      -- sidecar 上 storage 内的绝对路径
  cloud_file_id  TEXT,                               -- 辅助字段, 可能为空
  pickcode       TEXT,                               -- 115 辅助字段, 可能为空
  status         TEXT NOT NULL CHECK (status IN ('live','dead','pending')),
  last_seen      INTEGER NOT NULL,
  UNIQUE (sidecar_id, storage_mount, remote_path)
);
CREATE INDEX idx_file_copies_live ON file_copies(blob_id, status, last_seen DESC);

-- 跨 hash conflict: 不同 hash 引用看似同一物理文件但属性不一致
CREATE TABLE hash_conflicts (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  blob_id_a       INTEGER NOT NULL REFERENCES blobs(id),
  blob_id_b       INTEGER NOT NULL REFERENCES blobs(id),
  reason          TEXT NOT NULL,                     -- e.g. "size mismatch" / "name divergence" / "provider mismatch"
  detail          TEXT NOT NULL,                     -- JSON: 两边 size/name/provider/source 的 snapshot
  observed_at     INTEGER NOT NULL,
  status          TEXT NOT NULL DEFAULT 'open'      -- open / dismissed / merged
);
CREATE INDEX idx_hash_conflicts_status ON hash_conflicts(status, observed_at DESC);

-- Job 队列
CREATE TABLE jobs (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  kind          TEXT NOT NULL,                       -- ingest_manual / ingest_producer / restore_verify
  status        TEXT NOT NULL,                       -- pending/running/done/failed/cancelled
  payload       TEXT NOT NULL,                       -- JSON
  progress      TEXT,                                -- JSON {current,total,msg,warnings:[]}
  error         TEXT,
  owner_id      TEXT NOT NULL DEFAULT 'admin',
  created_at    INTEGER NOT NULL,
  started_at    INTEGER,
  finished_at   INTEGER
);
CREATE INDEX idx_jobs_status ON jobs(status, created_at);

-- Producer 子任务: 记录 exec 的 producer 工具一次运行 (审计 + 可重跑)
CREATE TABLE producer_runs (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id         INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  tool           TEXT NOT NULL,                      -- "115share2cas" / "cas139"
  tool_version   TEXT,                               -- producer --version 输出或 git sha
  cmdline        TEXT NOT NULL,                      -- 实际拼出的 argv (JSON 数组)
  workdir        TEXT NOT NULL,                      -- exec workdir
  output_dir     TEXT NOT NULL,                      -- CAS tree 输出目录
  manifest_path  TEXT,                               -- manifest.jsonl 路径
  stdout_path    TEXT,
  stderr_path    TEXT,
  exit_code      INTEGER,
  started_at     INTEGER NOT NULL,
  finished_at    INTEGER
);
CREATE INDEX idx_producer_runs_job ON producer_runs(job_id);

-- Job item dedup: 用 (library_id, rel_path, target_account, target_storage_mount, target_remote_dir, desired_name)
-- 作为 ingest job 内 item 的去重 key。粗细修正: 不只用 blob hash + target_account,
-- 同内容不同路径在同一 ingest job 内必须分别处理。
-- (Echo 在内存或临时表里实现,DDL 不强制持久化)
```

### `rel_path` 校验规则 (硬约束)

manifest 中的 `rel_path` 与 producer 输出的 .cas tree 目录结构都是**不可信输入**。Echo 在以下三个点必须重新校验,不能假设上游已经清洗过:

1. `POST /api/ingest/manual` handler 入口校验 `cas_tree_path` 与 `manifest_path` (落在 `producer.workdir_root` / 配置白名单根下)。
2. 遍历 manifest 时,**每一个 item 的 `rel_path` 都过 `validateRelPath()`**;失败的 item 直接进 `failed_items`,不阻塞 job。
3. 写本地 `.echo` 之前,**最终路径** (`library.echo_output_path + rel_path + ".echo"`) 还要做 symlink 解析后的 root 校验。

`validateRelPath(rel string) error` 规则:
- 空字符串 → 拒绝
- 长度 > 4096 → 拒绝
- 含 NUL / 任何 `< 0x20` 控制字符 → 拒绝
- 含 `\` 反斜杠段 / Windows 盘符 (e.g. `C:`) → 拒绝
- 任一路径分段为 `..` → 拒绝
- 绝对路径 (`/` 前缀) → 拒绝
- `filepath.Clean(rel)` 后再次跑上述全套规则 (clean 可能把 `a/b/../c` 变成 `a/c`,本身没问题;但若 clean 后出现 `..` 前缀必拒)

写本地 `.echo` 的额外校验 (`safeJoinUnderLibrary(library.echo_output_path, rel) (final string, error)`):
- `final := filepath.Join(library.echo_output_path, rel + ".echo")`
- `parent := filepath.Dir(final)`
- `evaluated, err := filepath.EvalSymlinks(parent)` (`os.MkdirAll(parent, 0755)` 先建好)
- `rel2, err := filepath.Rel(library.echo_output_path_evaluated, evaluated)` 其中 `library.echo_output_path_evaluated = EvalSymlinks(library.echo_output_path)`
- 若 `rel2` 以 `..` 开头或等于 `..` → 拒绝 (parent 通过 symlink 逃出 library root)
- `final` 自身禁止是 symlink (`lstat` 检查,O_NOFOLLOW 不在所有 fs 都可移植,显式拒绝即可)

远端路径 (`PutCASRequest.RemoteDir` / `file_copies.remote_path`) 不做 symlink 校验,但仍跑 `validateRelPath` 拒绝 `..` / 绝对路径 / 控制字符;sidecar 自身的 storage scope 是第二道防线。

### `.echo` 文件格式

`.echo` 是 base64 编码的 JSON payload,字段表与 `openlist-guangyapan-src/feat/cas-tools` 分支输出的 `.cas` **完全一致**(兼容超集,不是 OpenList-CAS 旧的 4 字段集):

| 字段 | 类型 | 说明 |
|---|---|---|
| name | string | 文件名 |
| size | int64 | 文件字节数 |
| provider | string | 来源 provider (115/139/189pc) |
| sha1 | string | 可选 |
| preID | string | 115 专用,可选 (JSON tag `preID`) |
| md5 | string | 可选 |
| sliceMd5 | string | 189pc 专用,可选 (JSON tag `sliceMd5`) |
| sha256 | string | 139 / 通用,可选 |
| create_time | string | 原始 mtime,可选 (JSON tag `create_time`,字符串而非 int,跟随上游 `casmeta.Payload`) |

Echo 同时识别 `.echo` 和 `.cas` 扩展名 (向后兼容 fork 里已有的 .cas 库),新写出统一用 `.echo`。

Encode/Decode 在 `castree/payload.go` 自实现,**不依赖 sidecar Go 包**。字段表层面对齐;如果 cas-tools 字段表演进 (加新 hash),Echo 跟随升级即可,无 Go module 耦合。

### `.echo` 写盘时机 (硬约束)

**Live copy 在 file_copies 表里写成 status=live 之后,才允许写 .echo 文件。**

不允许的状态:
- 占位文件存在但 file_copies 表无对应 live row (会导致 restore 时找不到副本)
- 占位文件已写但 sidecar restore 仍在 pending (用户播放命中半成品)

注意: SQLite/PG 事务**不能**回滚文件系统写入,所以 ingest 用"两段提交 + 启动 reconcile"而非单事务:

1. sidecar `PutCAS` 返回 `Status=restored` (或 `skipped_dup`) 后,**先**在一个 SQL 事务里:
   - `UPSERT file_copies ON CONFLICT(sidecar_id, storage_mount, remote_path) DO UPDATE`,仅刷 `status='live' / last_seen / cloud_file_id / pickcode`,**不改 blob_id**;若 existing.blob_id 与本次 candidate_blob 不一致 → 该 item 终止 (写 hash_conflicts、不写 library_entries、不写 .echo,详见 §4 步骤 3.d);
   - 一致时 `UPSERT library_entries(...echo_written=0)`;
   - COMMIT。
2. COMMIT 后,把 .echo payload 写到 `<library.echo_output_path>/<rel_path>.echo.tmp`,然后 fsync 并 rename 到最终路径。
3. rename 成功后,执行第二条 SQL: `UPDATE library_entries SET echo_written=1 WHERE id=?`。

崩溃恢复 (Echo 启动时执行 reconcile,在接收外部请求之前):
- `library_entries.echo_written=0` 且本地 .echo 不存在 → 重写 .echo,然后 `echo_written=1`。
- `library_entries.echo_written=0` 且本地 .echo 已存在 → 校验 hash 后直接 `echo_written=1`。
- 本地 `.echo.tmp` 残留 → 删除 (rename 未完成,内容不可信)。
- 本地 `.echo` 文件无对应 `library_entries` live row (孤儿) → 写 admin 告警日志,**不**自动删除 (避免误删用户手工放置的 .echo)。

### Blob 三层带来的影响

- **跨 library 复用**: 同一物理文件可以挂在多个 library 的不同路径,不重复 ingest
- **跨云副本**: 一个 blob 有多个 file_copy,restore 时按 last_seen 选最新
- **跨 hash conflict 不自动 merge**: v0.1 记录到 `hash_conflicts` 表 + admin UI 展示。merge 操作 (合并 file_copies、改 library_entries.blob_id、删冗余 blob) 留给后续 CLI 工具或人工 SQL,触发实例多了再实现自动化。

### v0.1 不做的: blob 自动 merge

如果同一物理文件先后以 sha1 和 md5 进入系统,会变成两个不同的 blob_id:

- T1: 115 ingest → blob(id=1, size=S) + blob_hashes(sha1=X, size=S)
- T2: 189 ingest 同一物理文件 → blob_hashes 查 sha1 未中、查 md5 也未中 → blob(id=2, size=S) + blob_hashes(md5=Y, size=S)

将来某 ingest 同时上报 (sha1=X, md5=Y, size=S),Echo 发现两个 hash 各自指向不同 blob,**写一行 hash_conflicts 而不自动 merge**:

- merge 涉及 file_copies/library_entries 重定向 + 删冗余 blob + 改 .echo (其实 .echo 内容不依赖 blob_id,只依赖 hash payload,所以这步零成本) + 日志
- 触发频率不明,自动化早做容易写错,先观察
- admin UI 显式列出 open conflict,用户点 "merge" 时再走专门的 RPC

## 4. 数据流

### Ingest 总览

Echo 不抓分享、不算 hash、不上传文件。 **CAS payload 由 producer 阶段产出** (`115share2cas` / `cas139` 经过真实 driver Put 流程,落到 .cas tree + manifest)。Echo 只**消费** CAS tree,委托 sidecar 在目标 storage 上做 rapid-only restore,落 live copy 后写 .echo。

两种入口:

1. **Manual import** — 用户已有 CAS tree (本机目录),Echo 直接消费
2. **Producer job** — Echo `os/exec` 调一个 producer 工具,等退出后消费它产出的 manifest + CAS tree

### Manual import 流程

```
POST /api/ingest/manual
Authorization: Bearer <ADMIN_TOKEN>
{
  "library_id": 1,
  "target_account": "115-main",
  "target_subdir": "movies/2026",
  "cas_tree_path": "/data/incoming/share-abc/",
  "manifest_path": "/data/incoming/share-abc/manifest.jsonl"
}
         |
         v
[handler]
  - 校验 library / account 存在
  - 校验 cas_tree_path 在白名单根目录下 (反路径穿越)
  - 插入 jobs(kind=ingest_manual, status=pending, payload=...)
  - 返回 {"job_id": 42}
         |
         v
[job runner pick up, status=running]
         |
         v
[ingest.pipeline.RunManual]
  1. castree.Reader 遍历 cas_tree_path,castree.Manifest 解析 manifest.jsonl
     for each manifest item: {rel_path, name, size, sha1, preID, ...}

  2. Dedup key 计算:
       (library_id, rel_path, account_id, storage_mount, target_subdir + rel_path 的 parent, basename)
     已经成功过的 item 跳过 (用 producer_runs 历史 + library_entries 现状判断)

  3. for each item (并发度 from config):
     **前置**: `validateRelPath(rel_path)` 失败 → 直接进 failed_items, 不进入下面流程。

     a. 候选 blob 解析:
        - 按 manifest 提供的每个 hash 类型查 blob_hashes (用规范化 hash_value_norm + size 联合查)
        - 收集所有命中行的 blob_id 集合 (按 created_at 升序去重)
        - 命中数 0 → 新建 blob(size, name),`candidate_blob = new.id`
        - 命中数 1 → `candidate_blob = 命中.blob_id`
        - 命中数 >1 → `candidate_blob = 最老的 blob_id`,把全部命中 blob_id 集合写一行 hash_conflicts(blob_id_a=最老, blob_id_b=其它, reason="hash_multi_blob", detail=JSON)
     b. 把 manifest 中已知 hash 写齐到 blob_hashes,**逐 hash 处理,不批量 INSERT**:
        - 查 blob_hashes(hash_type, hash_value_norm, size)
        - 未存在 → INSERT 进 candidate_blob (附带 size = blob.size)
        - 已存在且 `blob_hashes.blob_id == candidate_blob` → 幂等,skip
        - 已存在且 `blob_hashes.blob_id != candidate_blob` → **不写**,把这一条 hash + 竞争 blob_id 合并进当前 hash_conflicts.detail (或新写一行 reason="hash_owned_by_other_blob");候选 blob 不变,继续后面 sidecar 调用
        - 保证不会触发 UNIQUE(hash_type, hash_value_norm, size) 违例
     c. sidecar.PutCAS(PutCASRequest{
            StorageMount: account.storage_mount,
            RemoteDir:    join(target_subdir, dirname(rel_path)),
            CASName:      basename(rel_path) + ".cas",
            CASBody:      open(本 item 对应的 .cas 文件),
            CASSize:      stat.Size(),
        })
        sidecar driver.Put 内识别 .cas → rapid-only restore,返回 ItemResult
        - Status=restored → 进 (d)
        - Status=skipped_dup → 等同 restored, 但不记 warning (目标已存在)
        - Status=failed → ✗ 不写 file_copies, ✗ 不写 .echo,
                          job.warnings += 1,
                          item 进 jobs.progress.failed_items[],
                          **不允许 fallback 真实上传** (硬约束)
     d. 两段提交 + reconcile (SQL 事务不能回滚 fs 写,故拆开):

        段 1 (SQL 事务):
          - **UPSERT** file_copies ON CONFLICT(sidecar_id, storage_mount, remote_path) DO UPDATE
              SET status='live',
                  last_seen   = excluded.last_seen,
                  cloud_file_id = COALESCE(excluded.cloud_file_id, file_copies.cloud_file_id),
                  pickcode      = COALESCE(excluded.pickcode,      file_copies.pickcode)
            **不允许更新 `blob_id`**;若 existing.blob_id ≠ candidate_blob:
              · 不写 library_entries
              · 不写 .echo
              · 不更新 echo_written
              · 写一行 hash_conflicts(blob_id_a=existing.blob_id, blob_id_b=candidate_blob,
                  reason="copy_blob_mismatch",
                  detail=JSON{sidecar_id, storage_mount, remote_path, manifest_rel_path})
              · item 进 failed_items, job warning,
                admin 需要手工 (a) 换目标路径重试,或 (b) 在 admin UI 中解决冲突 (v0.2 提供工具,v0.1 走 SQL)
              · skipped_dup 同样走这条规则 — 这意味着 "目标已存在但 Echo 知道它属于别的 blob",一定是
                半成品/历史漂移,必须人工介入
            候选 blob 一致时才继续:
          - UPSERT library_entries(library_id, rel_path) DO UPDATE
              SET name=excluded.name, blob_id=excluded.blob_id, echo_written=0
            (注: library_entries.blob_id 允许在 candidate_blob 一致前提下更新,因为这就是同一 blob;
             不会出现把 entry 指向"无 live copy 的 blob")
          COMMIT

        段 2 (文件系统):
          - safeJoinUnderLibrary(library.echo_output_path, rel_path) → final (见 §3 校验规则)
          - echofile.Output.PutAtomic(final, payload_bytes)
            内部: 写 `<final>.tmp` → fsync → rename(.tmp → final)

        段 3 (SQL 单语句):
          - UPDATE library_entries SET echo_written=1 WHERE id=?

        任一段失败:
          - 段 1 UPSERT 因 copy_blob_mismatch 终止 → 已说明,item failed_items + hash_conflicts,
            live copy 在 sidecar 上保留 (它属于既有 blob,不动);
          - 段 2/3 失败 → DB live row 已存,echo_written=0 → 启动 reconcile 时重写 .echo (见 §3 写盘时机);
          - 段 1 其它 SQL 错误 (UNIQUE 之外) → 当前 item failed, job warning。

  4. jobs.progress 实时更新 (1s 节流)
         |
         v
完成: jobs.status=done (可能 warnings>0)
失败: jobs.status=failed, error=stack (一般是 sidecar 不可达 / DB 损坏等系统级)
```

### Producer job 流程

```
POST /api/ingest/producer
Authorization: Bearer <ADMIN_TOKEN>
{
  "library_id": 1,
  "target_account": "115-main",
  "target_subdir": "movies/2026",
  "tool": "115share2cas",
  "args": {
    "share_url":     "https://115.com/s/xxx?password=abc",
    "cookie_file":   "ref:cookies/115-main.txt",
    "mode":          "transfer-batch",
    "batch_size":    "1.5TiB",
    "recycle_password_file": "ref:secrets/115-recycle.txt"
  }
}
```

API args 一律 **snake_case**;Echo 内部映射到 115share2cas 的 CLI flag。完整白名单见 §6 配置。

**v0.1 producer args → CLI flag 映射**:

| API key | CLI flag | 是否必填 | 备注 |
|---|---|---|---|
| `share_url` | `--share-url` | 否 (二选一) | 与 `share_code`+`receive_code` 二选一;115share2cas 自动从 URL 解出 code/receive_code |
| `share_code` | `--share-code` | 否 (二选一) | 与 `share_url` 二选一 |
| `receive_code` | `--receive-code` | 视上面 | 若 `share_url` 已含 password,可省 |
| `cookie_file` | `--cookie-file` | mode=transfer-batch 必填 | 值必须是 `ref:<path-under-secrets-root>` |
| `mode` | `--mode` | 否 (默认 transfer-batch) | `transfer-batch` 或 `direct` |
| `batch_size` | `--batch-size` | 否 | 例: `1.5TiB` |
| `temp_parent_cid` | `--temp-parent-cid` | 否 | mode=transfer-batch 用 |
| `recycle_password_file` | `--recycle-password-file` | mode=transfer-batch 且 keep_temp=false 时必填 | 值必须是 `ref:` |
| `keep_temp` | `--keep-temp` | 否 | bool;true → CLI 加 `--keep-temp` |
| `limit` | `--limit` | 否 | int;0 表示不限 |

**API surface 不允许**传入: `--out` / `--manifest` (Echo 强制注入到 job workdir);`--page-size` / `--delay` / `--max-list-errors` / `--temp-root-name` / `--recycle-wait` / `--receive-chunk-size` / `--transfer-wait` / `--overwrite` / `--ua` (调优/兼容旋钮,首版不开放,减少测试矩阵与误用面)。

flow:

```
[handler]
  - 校验 tool ∈ {"115share2cas"} (白名单;v0.1 不在 producer-exec 路径支持 cas139,
    见下方说明)
  - 校验 args 中每个 key 都在上表里;snake_case 严格匹配
  - 校验组合: share_url 或 (share_code + receive_code) 至少一组;
            mode=transfer-batch 必须有 cookie_file;
            mode=transfer-batch 且 keep_temp 不为 true 必须有 recycle_password_file
  - 解析 ref: 引用 (cookie_file ref:xxx → 拼 producer.secrets_root + 路径校验, 不允许 .. / 绝对路径 / symlink 逃逸)
  - 插入 jobs(kind=ingest_producer, ...)
  - 返回 {"job_id": 43}
       |
       v
[job runner]
  1. 建临时 workdir: /data/ingest/job-43/
  2. 拼 argv (按映射表把 snake_case key → kebab CLI flag, 注入 --out / --manifest):
       115share2cas --share-url 'https://115.com/s/xxx?password=abc'
                    --cookie-file /data/secrets/cookies/115-main.txt
                    --recycle-password-file /data/secrets/secrets/115-recycle.txt
                    --mode transfer-batch
                    --batch-size 1.5TiB
                    --out /data/ingest/job-43/cas/
                    --manifest /data/ingest/job-43/manifest.jsonl
  3. 写一行 producer_runs(job_id, tool, tool_version, cmdline, workdir,
                            output_dir=/data/ingest/job-43/cas/,
                            manifest_path=/data/ingest/job-43/manifest.jsonl,
                            stdout_path=/data/ingest/job-43/stdout.log,
                            stderr_path=/data/ingest/job-43/stderr.log,
                            started_at=now)
  4. os/exec: 工作目录限制在 workdir, env 白名单, stdin /dev/null
  5. wait → UPDATE producer_runs SET exit_code, finished_at
  6. 如果 exit_code != 0 → job failed, 保留 stdout/stderr 供 admin 查
  7. 否则 → 与 manual 同路径消费 manifest + CAS tree
       (从步骤 2 起复用 Manual import 流程)
```

**为什么 cas139 不在 v0.1 producer-exec 路径**:

上游 `tools/cas139/{single,multi}_share_to_cas_batch.py` 的实际行为是把 `.cas` 写到 **139 云端**的输出目录,本地无 `--out` / `--manifest` 输出选项 (真实参数是 `--share-id` / `--files-json` / `--out-dir-name` / `--progress` 等)。Echo 不能像 115share2cas 那样"等退出后消费本地 manifest + .cas tree"。

v0.1 处理方式: 139 走 **manual import only** — 用户在 139 自行跑 cas139 → 把生成的 `.cas` tree 从 139 云端下载到本地 → 调 `/api/ingest/manual` 指向本地路径。把 cas139 的 Echo-side 自动化挪到 v0.1.x / v0.2,届时需要单独设计 "consume CAS tree from remote storage" 路径 (例如让 sidecar 把 139 storage 上的 .cas tree mount 出来,再让 Echo 通过 sidecar `/api/fs/list` 走 manifest)。

**安全约束**:
- tool 白名单, 不接受任意可执行文件
- 每个 tool 的 flag 白名单写在 Echo 代码里, 不接受 free-form 字符串
- 凭据用 `ref:` 引用本地受控目录, 不让 API 直接传 cookie 内容 (避免日志泄露)
- 不做被动目录监听 (容易半成品 + 重复导入)

### Restore 总览 (双 endpoint)

#### `GET /api/restore/{file_id}?prefer=115`

返回 JSON,不重定向。给"我自己拿 URL 自己访问"的客户端用 (比如 Echo 自己的 Web UI 预览、第三方脚本)。

```
[handler]
  1. 查 library_entries(file_id) → blob_id
  2. 查 file_copies WHERE blob_id=? AND status='live'
     ORDER BY (provider=prefer DESC), last_seen DESC LIMIT 5
  3. 取队首 copy: 调 sidecar.Link(storage_mount, remote_path)
     - 200 → 返回 JSON {url, headers, expires_at, provider, copy_id}
     - sidecar 报 not found / 410 → 标 copy.status=dead, 试下一个
     - sidecar 5xx / 网络错误 → 不改 status, 试下一个, 记 warning
  4. 全部失败:
     - v0.1: 404 + JSON {dead_copies:[...]}, headers 含 X-Echo-Reason
     - v0.2 可选: 触发 restore-on-demand job 从其他副本秒传到 prefer
```

#### `GET /api/stream/{file_id}?prefer=115`

由 Echo 反向代理 sidecar 拉到的字节流。给 Emby / 通用客户端用 (它们不知道怎么处理 OpenList 直链的签名 / cookie / UA 要求)。

```
[handler]
  1. 同 restore handler 步骤 1-2, 选出候选 copy 队列
  2. 取队首 copy, 构造 StreamRequest:
       StorageMount: copy.storage_mount
       RemotePath:   copy.remote_path
       Headers:      从客户端请求复制白名单子集 ──
                       Range / If-Range / If-Modified-Since / If-None-Match / User-Agent
                     (其它 header 不透传, 避免泄露 admin token / Cookie)
     调 sidecar.Stream(StreamRequest) → *StreamResult
     - sidecar 自己向源站请求并把上游 Response 透传 (含 206 / Content-Range)
     - sidecar 报 not found / 410 → 标 copy.status=dead, 试下一个 copy
     - sidecar 5xx / 网络错误 → 不改 status, 试下一个 copy, 记 warning
  3. 把 StreamResult 写回客户端:
       w.WriteHeader(result.StatusCode)         -- 200 / 206 / 304 / 416 透传
       for k,v := range result.Header { ... }    -- Content-Length / Content-Range / Content-Type /
                                                    Accept-Ranges / Last-Modified / ETag 透传
       if result.Body != nil:
         io.Copy(w, result.Body); result.Body.Close()
  4. 中途断流 → 客户端报错, Echo 记 warning, 不自动重试 (客户端会自己用 Range 续传)
  5. 全部 copy 失败 → 404 + X-Echo-Reason

注: Echo 不在自己进程内做"打源站"那条 fallback (sidecar 是统一直链失效逻辑入口)
```

v0.1 restore/stream 入口**只接受 `file_id`**,不提供"按本地 .echo 路径反查"接口 (本地 path 入参会变成任意 .echo/.cas 读取与资源枚举攻击面)。详见 §13。

### 关键决策

| 决策 | 选择 | 理由 |
|---|---|---|
| Restore 失败 fallback 真实上传 | **拒绝**, 整个 item failed | Echo 是 CAS-only 控制面, 真上传违背项目定位 (放在 §5 详述) |
| Sidecar restore 单文件失败 | 跳过该文件 + 记 warning, 不阻塞 job | 跨云资源会陆续到位, 单点失败不该回滚整个 ingest |
| 同 blob 跨账号去重 | 多行 file_copies 共享同一 blob_id | 自然反映"一个 blob, 多云副本" |
| 跨 hash conflict | v0.1 不自动 merge, 写 hash_conflicts | 复杂逻辑, 等真实数据触发再实现 |
| `.echo` 写盘时机 | live copy COMMIT 后写,两段提交 + 启动 reconcile | 杜绝"占位存在但 restore 找不到"半成品;fs 写无法随 SQL 事务回滚故拆开 |
| `.echo` 写盘位置 | library 级配置, v0.1 仅 local fs | 简化首版; v0.2 加 WebDAV (Emby 远程扫描场景) |
| 直链缓存 | memory TTL=60s, key=(blob_id, copy_id) | 性能优化; sidecar 自身的 link 复用窗口决定下限 |
| Restore 双 endpoint | JSON + proxy 两个 | 给不同客户端类型用; Emby 必须代理 |
| Job 进度持久化 | SQLite, 1s 节流写 | 重启可见,但 running job 不自动续 (标 failed) |
| Job 并发 | 单 job 内 N worker (默认 4) + 多 job 排队 (默认 4 并发) | 简单可调,环境变量覆盖 |
| Producer 输入 | 白名单 tool + 白名单 flag + ref: 凭据 | 防 shell 注入 + 凭据不进 API |
| 被动目录监听 | **不做** | 容易半成品和重复导入 |
| Dedup key 粒度 | (library_id, rel_path, target_account, storage_mount, target_remote_dir, desired_name) | 不只用 hash + account, 否则误跳同内容不同路径 |

## 5. 错误处理与降级

### 分层错误类型

```go
// store 层
var (
    ErrNotFound    = errors.New("not found")
    ErrConstraint  = errors.New("constraint violation")
)

// sidecarclient 层
var (
    ErrSidecarUnreachable   = errors.New("sidecar unreachable")
    ErrSidecarVersionTooOld = errors.New("sidecar version older than required")
    ErrStorageNotFound      = errors.New("storage not registered on sidecar")
    ErrCASRestoreFailed     = errors.New("sidecar refused or failed CAS restore")
    ErrLinkNotAvailable     = errors.New("sidecar returned no direct link")
)

// ingest / restore 层
var (
    ErrManifestInvalid     = errors.New("manifest unreadable or malformed")
    ErrCASTreeInvalid      = errors.New("cas tree missing or unreadable")
    ErrProducerUnauthorized = errors.New("producer tool or flag not in whitelist")
    ErrProducerExitFailed   = errors.New("producer process exited non-zero")
    ErrAllCopiesDead        = errors.New("no live copy available")
)
```

handler 层映射到 HTTP:
- ErrNotFound → 404
- ErrSidecarUnreachable → 503 + admin 提示 (readyz 同步降级)
- ErrSidecarVersionTooOld → 503 + 明确版本要求字符串
- ErrManifestInvalid / ErrCASTreeInvalid / ErrProducerUnauthorized → 400
- ErrAllCopiesDead → 404 (含 dead_copies 列表)
- 其余 → 500 + 日志

### 硬约束: 拒绝 fallback 真实上传

Echo **不能**在 ingest 失败时改走"真实数据上传"。任何 sidecar 上的 driver.Put(FileStreamer) 调用都不在 Echo 的执行路径内。

具体含义:
- sidecar `PutCAS` 返回 `Status=failed`,Echo **不再调** sidecar 的其他 endpoint 试图上传该文件
- Echo 不接受 multipart 文件上传 API,API surface 上没有"上传文件"的入口
- 即使用户在 Web UI 上请求重试,也只会重跑 CAS restore 路径 (重读 manifest, 重 PUT 每个 .cas 到 sidecar),不会"用真实数据替代"

理由:
- Echo 是 CAS-only 控制面,真实上传违背项目定位 (NextEmby/Rose 才做真实秒传转存,且都是用户账号的真实数据流)
- "失败给人看清"比"假装成功"重要,跨云资源会陆续到位,单点失败应该让 admin 看到
- 真实上传需要原始字节流,Echo 设计上完全脱离原始字节,引入会撕裂分层

未来若需要"自动用其他云的真实文件补一份到目标云",那是**另一个独立路径** (从其他云 sidecar 下载 → 上传到目标云 sidecar),需要单独设计、单独命名、单独 API。**不**作为 ingest 失败的隐式 fallback。

### 降级策略

| 场景 | 策略 |
|---|---|
| Ingest 单文件 sidecar restore 失败 | item failed, 进 jobs.progress.failed_items[], 不阻塞同 job 其他 item;**不 fallback 真实上传** |
| Ingest 整个 manifest 无 item 成功 | job 仍标 done (warnings 满标), 让 admin 看到全部 failed item;不自动重试 |
| Restore 单 copy 失败 | 标 copy.status=dead, 自动 fallback 下一个 copy, 全死返 404 |
| Sidecar token 过期 | **sidecar 自身负责 refresh**;Echo 收到 ErrCASRestoreFailed 时不主动管 token, 只更新 account.status=token_expired 并提示 admin 去 sidecar UI 处理 |
| Sidecar 不可达 (网络断 / 进程死) | readyz fail, 所有 ingest/restore 请求 503, Echo 不缓存重试 |
| Sidecar 版本太老 (不满足 API 契约) | readyz fail, 启动时日志告警 (具体所需版本号) |
| `.echo` 输出失败 (disk full / WebDAV 401) | 段 2 (fs PutAtomic) 报错,DB live row 已落但 echo_written=0;job warning;启动 reconcile 时重试写 .echo (见 §3 写盘时机) |
| 进程崩溃 | 启动时把 running job 标 failed + error="interrupted", 人工重跑 |
| 跨 hash conflict | 写一行 hash_conflicts, 取较老 blob_id 继续, 不阻塞 |
| Producer exit != 0 | job failed, 保留 stdout/stderr, **不**消费已部分产出的 manifest (避免半成品) |
| Producer 卡住 | 配置级 timeout (默认 6h, 可覆盖), 超时 kill -TERM → kill -KILL, 标 job failed |

### 不做的

- 不做自动 retry-with-backoff (失败给人看清比假装成功重要)
- 不做 circuit breaker (account.status 就是手工断路器)
- 不做 sidecar 自动重启 (运维职责, 由 docker / systemd 管)
- v0.1 不做跨进程分布式锁 (单 Echo 实例假设;sidecar 单实例假设)
- 不做"假装秒传"的桩 (任何 Stub mode 都禁止合并到主分支,只允许在 integration test 用 sidecar fake server)

## 6. 配置与凭据

### config.yaml

```yaml
server:
  bind: ":8080"
  read_timeout: 30s
  write_timeout: 60s

database:
  path: /data/echo.db
  # 未来扩展:
  # driver: postgres
  # dsn: postgres://...

auth:
  admin_token: ${ECHO_ADMIN_TOKEN}     # v0.1 静态 token

sidecar:
  # v0.1 单 sidecar; 后期可扩展为 list
  default:
    base_url:        http://sidecar:5244
    auth_token_env:  ECHO_SIDECAR_TOKEN   # sidecar 的 admin token (从 env 取)
    min_version:     "feat/cas-tools@<commit-sha-or-tag>"
    request_timeout: 60s
    stream_timeout:  10m

producer:
  workdir_root:   /data/ingest        # producer job 临时目录根
  secrets_root:   /data/secrets       # cookie 文件等敏感文件根 (只读)
  default_timeout: 6h
  tools:
    "115share2cas":
      binary:        /usr/local/bin/115share2cas
      # API args key (snake_case) → CLI flag 映射见 §4。
      # --out / --manifest 由 Echo 强制注入,不在白名单。
      # 以下是 v0.1 暴露给 API 的子集 (其它 CLI flag 显式不开放):
      api_args_allowlist:
        - "share_url"
        - "share_code"
        - "receive_code"
        - "cookie_file"
        - "mode"
        - "batch_size"
        - "temp_parent_cid"
        - "recycle_password_file"
        - "keep_temp"
        - "limit"
    # v0.1 不在此自动化 cas139: 工具产物落 139 云端,无本地 manifest 输出。
    # 139 走 manual import (用户自行跑 cas139 → 下载 .cas tree → /api/ingest/manual)。

jobs:
  max_concurrent: 4
  worker_per_job: 4

echo_output_defaults:
  kind: local
  base_path: /data/output

log:
  level: info
  format: json                          # json / text
```

环境变量覆盖任何 yaml 字段 (`ECHO_SERVER_BIND` 等)。

### 凭据存储 (核心决策: 不放在 Echo)

云盘账号的真实凭据 (cookie / refresh_token / API key) **存放在 sidecar 上**, 由 sidecar 自己管理加密与刷新。Echo DB 的 `accounts` 表只持有 sidecar 上 storage 的引用:

```json
{
  "id":            "115-main",
  "provider":      "115",
  "sidecar_id":    "default",
  "storage_mount": "/115-main",
  "status":        "ok",
  "last_check":    1716873600
}
```

这样:
- Echo dump DB 不暴露任何云盘凭据
- 凭据轮换发生在 sidecar UI / config, Echo 透明跟进
- Echo 不再需要 AES-GCM master key (`ECHO_MASTER_KEY` 删除)

### Producer 凭据 (例外: cookie 文件)

`115share2cas` 等 producer 工具需要 cookie 文件 (走 driver Put 上链路, 不是 sidecar 自身)。这些文件由 admin **手动放入** `producer.secrets_root` 目录 (e.g. `/data/secrets/cookies/115-main.txt`), API 通过 `ref:` 引用:

```json
{
  "tool": "115share2cas",
  "args": { "cookie-file": "ref:cookies/115-main.txt" }
}
```

Echo 在 exec 时把 `ref:cookies/115-main.txt` 解析为 `/data/secrets/cookies/115-main.txt`, 防止越权 (`..`、绝对路径、symlink) 写在 ingest handler 校验里。

如果未来希望 Echo 也管理这些 cookie 文件, 加一个 admin upload endpoint, 仍存放在 `secrets_root`, 通过 `ref:` 引用使用。**API surface 不直接接受 cookie 字符串**, 避免日志泄露。

## 7. 测试策略

### 单元测试 (CI 必跑)

| 包 | 测试重点 |
|---|---|
| `store/*` | DDL migration、CRUD、UNIQUE 触发 (含 `hash_value_norm + size` 联合)、级联删除、并发安全 |
| `castree/{reader,manifest,payload}` | manifest fixture 解析, payload base64 JSON encode/decode round-trip, 与 `feat/cas-tools` 输出互通校验 |
| `sidecarclient/*` | `httptest` 起 fake sidecar, 验证 Ping/Version/ListStorages/PutCAS/Link/Stream 各分支 (200/404/410/5xx/timeout) |
| `ingest/pipeline` | mock sidecarclient + mock store, 验证 dedup key / 跨 hash conflict / restore failed item 不阻塞 / .echo 事务回滚 各分支 |
| `ingest/producer` | mock exec wrapper, 验证 tool/flag 白名单、`ref:` 解析、workdir 隔离、exit_code 处理、stdout/stderr 持久化 |
| `restore/{resolver,proxy,cache}` | mock sidecarclient, 验证 copy fallback、stream 代理 header 透传、cache TTL |
| `echofile/output` | local fs Put / Mkdir / List, 路径穿越拒绝 |

### 集成测试 (build tag `integration`, CI 可选)

| 测试 | 描述 |
|---|---|
| Real sidecar + 115 ingest | 启 sidecar 容器 + 真 115 账号 + 测试分享 → producer + manifest → Echo ingest → 校验 DB + .echo + 实际 storage 上文件存在 |
| Real Restore JSON | 已 ingest 的 file_id → 调 `/api/restore/{id}` → 验证 JSON 含 url + headers, url 可下载 |
| Real Restore Stream | 调 `/api/stream/{id}` → 验证字节流与 Range 请求正常 |
| Multi-job concurrency | 10 个 ingest job 并发, 验证数据隔离、并发上限、sidecar 调用稳定 |
| Sidecar 不可用降级 | 杀掉 sidecar 容器, 验证 readyz fail / ingest 请求 503 |

集成测试本地手跑: `go test -tags=integration ./integration/...`。CI nightly (有真账号 secret 的分支才跑)。

### 测试工具

- `testify/assert` + `testify/mock`
- `httptest` 模拟 sidecar HTTP 响应 (含 chunked stream、Range、5xx)
- goldenfile: manifest / payload 解析期望输出存 `testdata/*.golden`
- fake sidecar harness: 一个轻量 Go 服务模拟 sidecar 的最小 API 表面, 给 integration 与 e2e 用

## 8. 监控与运维

### 内置 endpoint

| Path | 用途 |
|---|---|
| `GET /healthz` | liveness, 只看进程存活 |
| `GET /readyz` | readiness: DB ping + sidecar Ping + sidecar 版本满足 |
| `GET /metrics` | Prometheus 格式 |

### Metrics 埋点

```
echo_jobs_total{kind, status}                            counter
echo_ingest_items_total{provider, result}                counter
   result: restored / skipped_dup / failed / parse_error
echo_restore_requests_total{provider, endpoint, result}  counter
   endpoint: json / stream
echo_restore_latency_seconds{provider, endpoint}         histogram
echo_sidecar_calls_total{sidecar, method, status}        counter
   method: ping / version / list_storages / restore_from_cas / link / stream
echo_sidecar_call_latency_seconds{sidecar, method}       histogram
echo_producer_runs_total{tool, result}                   counter
   result: success / exit_failed / timeout / unauthorized
echo_db_open_connections                                 gauge
echo_account_status{provider, account_id}                gauge  (ok=1 expired=0 banned=-1 unknown=2)
echo_hash_conflicts_open                                 gauge
```

### 日志

- `log/slog` (Go 1.21+ 标准库)
- 默认 JSON 输出到 stdout (docker 友好)
- 配置可切 text 格式 (开发场景)
- 关键事件: account 状态变化、job 状态变化、跨 hash conflict 新增、producer exec 启停、sidecar 健康度变化
- **不在日志里打印** cookie / token / sidecar auth token / 直链 URL 的 sign 参数

## 9. 部署

### docker-compose.example.yml

```yaml
services:
  sidecar:
    image: ghcr.io/xmm2022/openlist-guangyapan:feat-cas-tools-<sha>
    ports:
      - "5244:5244"
    environment:
      OPENLIST_ADMIN_TOKEN: ${ECHO_SIDECAR_TOKEN}
    volumes:
      - ./sidecar-data:/opt/openlist/data    # sidecar 自己的 DB + 凭据
    restart: unless-stopped

  echo:
    image: ghcr.io/xmm2022/echo:latest
    depends_on:
      - sidecar
    ports:
      - "8080:8080"
    environment:
      ECHO_ADMIN_TOKEN:     ${ECHO_ADMIN_TOKEN}
      ECHO_SIDECAR_TOKEN:   ${ECHO_SIDECAR_TOKEN}
    volumes:
      - ./data:/data
      - ./output:/data/output           # .echo 文件输出
      - ./ingest:/data/ingest           # producer job 临时目录
      - ./secrets:/data/secrets:ro      # cookie 等敏感文件 (admin 手动放置)
    restart: unless-stopped

  # 可选: nginx/caddy 反代
```

注: producer 工具中 `115share2cas` 已编译进 Echo 镜像 (CI 期从 sidecar 仓库的 `feat/cas-tools` 分支抓 build artifact)。`cas139` (Python) v0.1 **不**打包进 Echo 镜像 — 它需要在 139 客户端环境跑,用户自行准备运行环境,产物下载到本地后走 manual import。Echo 镜像与 sidecar 镜像各自独立 release,producer 版本固定到 Echo 镜像的 release notes。

### 反代 (nginx 示例)

```nginx
location /echo/ {
  proxy_pass http://echo:8080/;
  proxy_set_header Host $host;
  proxy_set_header X-Real-IP $remote_addr;
  client_max_body_size 1m;              # Echo API 只接 JSON body, 没有文件上传入口

  # 流式 endpoint 不缓冲
  proxy_buffering off;
  proxy_read_timeout 1h;
}
```

### 备份

- SQLite 文件 (`/data/echo.db`)
- `.echo` 输出目录 (`/data/output`)
- producer cookie 文件 (`/data/secrets/cookies/`,**严格 admin only**)
- sidecar 自己的数据 (`./sidecar-data`,凭据在这里, 独立加密)

凭据丢失影响: sidecar `./sidecar-data` 丢失则全部 storage 需要重新绑定。Echo `/data` 丢失则 ingest 历史与 library 树丢失, 但可以从 .echo 文件重新喂回。

### 单实例上限

- SQLite 推荐 ≤ 10 万 blob / 10 万 copies;更大量级切 PostgreSQL
- 单 Echo 进程: 默认 4 并发 job × 4 worker = 16 个并发 sidecar restore / link 调用
- v0.1 假设单 Echo 实例 + 单 sidecar 实例, 不做分布式锁
- sidecar 自身的并发上限由 sidecar 配置决定, Echo 不约束

## 10. License

Echo 遵循 AGPLv3。详见 `LICENSE` 文件 (AGPLv3 全文)。

### AGPLv3 §13 对网络服务的源码提供义务

> "Notwithstanding any other provision of this License, if you modify the Program, your modified version must prominently offer all users interacting with it remotely through a computer network ... an opportunity to receive the Corresponding Source of your version by providing access to the Corresponding Source from a network server at no charge, through some standard or customary means of facilitating copying of software." — AGPLv3 §13

简明含义 (本项目的解读):

> Echo 遵循 AGPLv3。**网络交互本身并不等同于 GPL 意义上的 convey / distribution**, 但 AGPLv3 §13 对**修改版**网络服务额外规定了源码提供义务: 若您部署的是经过修改的 Echo 版本, 并允许用户通过计算机网络与该修改版交互, 则您必须向这些远程用户提供获取该修改版完整对应源码 (Corresponding Source) 的明确入口。
>
> 部署未经修改的 Echo 不触发该额外义务, 但仍受 AGPLv3 §4 - §6 (源码可用、保留版权声明、衍生作品继续 AGPLv3) 约束。
>
> 参见 https://www.gnu.org/licenses/agpl-3.0.html

> 注: 上述为项目作者对许可证文本的简明陈述, 不构成法律意见。AGPLv3 的权威文本以 GNU 官方版本为准。

### 选择 AGPL 的理由

1. **生态对齐**: OpenList、OpenList-CAS、openlist-guangyapan-src 全部 AGPLv3。Echo 通过 sidecar HTTP 调用使用上游服务, 严格意义上 *Echo 自身* 不会因为 HTTP 调用关系自动被 AGPL "传染"; 但本项目主动选择 AGPLv3, 跟上游生态一致, 避免下游对许可证关系产生歧义
2. **保证"第三选择"永久免费**: copyleft 让任何 fork / SaaS 衍生品都必须开源, 防止 Echo 后续被任意闭源化
3. **明确边界**: 跟闭源的 NextEmby / RoseHub 形成 license 层面的对比

### License 链条 (向上游 + 周边工具)

| 项目 | License | 角色 |
|---|---|---|
| Echo (本项目) | AGPLv3 | 资源池控制面 |
| openlist-guangyapan-src (xmm2022) | AGPLv3 | 多云 CAS sidecar |
| ├─ `cmd/115share2cas` (feat/cas-tools 分支) | AGPLv3 (随仓库) | 115 分享 → CAS tree 产生器 |
| └─ `tools/cas139` (feat/cas-tools 分支) | AGPLv3 (随仓库) | 139 分享 → CAS tree 产生器 |
| OpenList-CAS (GitYuA) | AGPLv3 | 189pc CAS 模式起源 |
| OpenList (OpenListTeam) | AGPLv3 | 通用网盘挂载框架 |

### 致谢与版权声明承担位置

AGPLv3 §5 要求保留版权声明。Echo 在以下位置承担:

- `LICENSE` 文件 (AGPLv3 全文)
- `README.md` 致谢章节,显式列出:
  - **CAS 模式** 来自 GitYuA/OpenList-CAS (189pc 起源)
  - **CAS 多云扩展 + 115/139/光鸭 driver** 来自 xmm2022/openlist-guangyapan-src
  - **CAS tree producer 工具** (`115share2cas`, `cas139`) 来自 openlist-guangyapan-src 的 `feat/cas-tools` 分支
  - **OpenList 通用框架** 来自 OpenListTeam/OpenList
- `castree/payload.go` 顶部注释标明字段表对齐自 `feat/cas-tools` 的 `pkg/casmeta`
- 任何派生关系在源码注释中标明

### 第三方间接 license

Echo 自身依赖 (Go module, 直接):
- `chi` — MIT
- `templ` — MIT
- `golang-migrate` — MIT
- `sqlc` 生成代码 (sqlc 本体 MIT, 生成代码归本项目)
- 通用 Go 工具库 (slog/etc) — Go 标准库 / MIT / Apache

通过 sidecar HTTP 边界, Echo **不引入** sidecar 自身的 Go module 依赖 (115driver / 115-sdk-go / resty / etc), 这些约束保留在 sidecar 进程内, 不污染 Echo 的依赖树。

所有 Echo 直接依赖与 AGPLv3 兼容。

## 11. v0.1 → v0.2 → v0.3 路线

```
v0.1   ── ingest + restore + 最小后台 ── Echo 资源池服务可独立使用
   │
   ▼
v0.2   ── Rose-like 媒体代理 ──────────── Emby 反代 + PlaybackInfo 改写
                                             + 多用户 Cookie 池
                                             + per-user 配额追踪
   │
   ▼
v0.3   ── NextFind-like 自动订阅 ────── TG / 海报站爬虫 + 规则评分
                                             + TMDB 订阅入库
```

每个版本独立 release。v0.2 和 v0.3 是消费 v0.1 manifest 的应用层,与 v0.1 核心模块松耦合。

## 12. 已确认的设计决策汇总

### 架构 (sidecar 模型)

- 项目名: **Echo**
- 形态: **控制面 (Echo) + 执行面 (OpenList sidecar) 双进程**, HTTP REST 通信
- Echo 自身: **不实现 driver, 不抓分享, 不算 hash, 不真实上传文件**
- 与上游 `openlist-guangyapan-src` 的关系: **HTTP 边界**, 不通过 Go module import
- Sidecar 仓库: `github.com/xmm2022/openlist-guangyapan-src` (以 GitHub remote 为准, 不仅看本地 fork)
- Sidecar 版本兼容: Echo 启动时探测 sidecar 版本, 不满足则 readyz fail

### 语言与基础栈

- 语言: **Go 1.22+**
- HTTP 框架: `chi` (轻量、标准库友好)
- 模板: `templ` (Go 类型安全模板) + htmx 渐进增强
- 日志: `log/slog`
- 数据库访问: `sqlc` (代码生成,类型安全)
- 迁移: `golang-migrate`
- 数据库: **SQLite** (v0.1) → PostgreSQL (后期可选)
- License: **AGPLv3** (跟 OpenList / OpenList-CAS / openlist-guangyapan-src 生态对齐)

### 文件格式与数据模型

- 文件格式: **`.echo`** (同时识别旧 `.cas`, 字段表与 `feat/cas-tools` 分支 `pkg/casmeta` 对齐)
- `.echo` 字段表: name / size / provider / sha1 / preID / md5 / sliceMd5 / sha256 / create_time (与 `casmeta.Payload` JSON tag 一致;`create_time` 为 string)
- payload Encode/Decode: Echo 在 `castree/payload.go` **自实现**, 字段表层面对齐, 不依赖 sidecar Go 包
- 数据模型: **三层拆分** — `blobs` (物理指纹) / `library_entries` (库内路径) / `file_copies` (云端 live copy)
- 辅助表: `blob_hashes` (多 hash 索引, UNIQUE(hash_type, hash_value_norm, size)) / `hash_conflicts` (跨 hash 冲突记录, 不自动 merge)
- `file_copies` 关键字段: `sidecar_id / storage_mount / remote_path / provider / account_id / status / last_seen`;`cloud_file_id / pickcode` 仅作辅助
- 资源指纹去重: `hash_value_norm`(规范化) + size, 防止跨 size 误匹配

### Ingest

- 两种入口: **Manual import** (已有 CAS tree) + **Producer job** (Echo exec 调 `115share2cas`;cas139 走 manual import,不在 v0.1 producer-exec 路径)
- **Echo 不生成 CAS payload**, payload 必须由 producer 工具产出 (因为 hash 来自真实文件经 driver Put 流程的副产物)
- Producer 白名单: tool ∈ {115share2cas}, 每个 tool 的 flag 也走白名单
- Producer 凭据: 用 `ref:` 引用本地受控目录, 不让 API 传 cookie 内容
- Producer 审计: 记录 cmdline / version / exit_code / stdout / stderr / output_dir / manifest_path 到 `producer_runs` 表
- **被动目录监听**: 不做
- **真实文件上传 fallback**: 不做 (硬约束, 在 §5 详述)
- Job 失败策略: 单 item failed 不阻塞 job, job warning 数累加, **绝不 fallback 真实上传**
- 跨 hash 冲突: 写 `hash_conflicts`, 取较老 blob_id 继续
- Job 并发: 单 job 内 N worker (默认 4) + 多 job 排队 (默认 4 并发)
- Dedup key 粒度: `(library_id, rel_path, target_account, storage_mount, target_remote_dir, desired_name)` (不只用 hash + account)
- `.echo` 写盘时机: **live copy COMMIT 后写, 两段提交 + 启动 reconcile**, fs 写无法随 SQL 事务回滚, 启动时检查 echo_written=0 的 entry 重写

### Restore

- 双 endpoint:
  - `GET /api/restore/{file_id}` → JSON `{url, headers, expires_at}` (给客户端自访问)
  - `GET /api/stream/{file_id}` → Echo 代理 sidecar 流 (给 Emby/通用客户端)
- 副本选择: prefer 偏好 + last_seen DESC
- 直链缓存: memory TTL=60s, key=(blob_id, copy_id)
- 单 copy 失败标 dead, 自动 fallback;全死返 404

### Provider 范围

- v0.1: **115 / 139 / 189pc**
- 光鸭网盘: **不承诺** (sidecar 已有 driver, Echo 不主动支持)
- 阿里 / 百度 / 夸克: v0.1 不支持 (等社区秒传协议成熟)

### 安全 & 凭据

- 静态 `ADMIN_TOKEN` (v0.1 单用户) + middleware
- 凭据**存在 sidecar 上**, Echo DB 只引用 sidecar storage mount
- producer cookie 文件: 放在受控目录 (e.g. `/data/secrets/cookies/`), 走 `ref:` 引用

### 用户系统

- v0.1 **不做**;`owner_id` 列预留 + auth middleware 占位;v0.2 替换

### License & 致谢

- AGPLv3, 文字参 GNU §13 (网络交互不等同 GPL convey;但修改版网络服务必须提供 Corresponding Source)
- 致谢链: OpenList → OpenList-CAS → openlist-guangyapan-src (含 feat/cas-tools 的 `115share2cas` / `cas139`) → Echo
- Echo 不引入 sidecar 自身的 Go module 依赖 (HTTP 边界隔离)

### 已否决的旧方案 (避免回头讨论)

- ~~Go module import `openlist-guangyapan-src` + `pkg/casmeta` 重构~~ — `internal/*` 不可外部 import, driver 接口暴露 `internal/model` 类型, 不可行
- ~~`driverpool/adapter.go` 在 Echo 进程内复用 driver 生命周期~~ — 旁路 sidecar 的 storage manager 会长期分叉
- ~~RapidUpload(hash-only) 作为统一 driver 接口~~ — driver.Put 实际接 FileStreamer, 只有 sidecar 内的 `cas_restore.go` 是 hash-only 路径
- ~~302 redirect 作为唯一 restore 形态~~ — Emby 需要 Echo 代理流, 单 JSON+302 不够
- ~~已挂载路径泛化作为 ingest 入口~~ — 189pc 之外语义不一致, v0.1 不做
- ~~MIT + 自写 thin driver~~ — 用户决定保持开源 + 与上游 AGPL 生态一致

## 13. v0.1 不解决的问题(留给后续版本)

- 多用户 / 多租户(v0.2)
- 用户配额追踪(v0.2)
- Emby 集成(v0.2)
- 资源订阅 / 自动找回(v0.3)
- 跨实例分布式(v1.0+)
- file_id 自动 merge 工具(按需,触发后再做)
- `by-echo` 路径反查接口 (按本地 .echo 路径调 restore/stream)(v0.2+):v0.1 仅 `file_id` 入口;后续若加回,必须改用 `?library_id=<int>&rel_path=<rel>` 形式,服务端按 library root 解析 + §3 rel_path 校验,**不允许**接受任意本地绝对路径
- 阿里云盘 / 百度网盘 / 夸克 等新 provider(等社区秒传协议成熟)
