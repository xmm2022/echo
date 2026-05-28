# Echo v0.1 设计

- 日期: 2026-05-28
- 作者: Echo 项目
- 状态: 设计稿,待评审

## 1. 项目定位

Echo 是一个跨云盘资源池服务,把多家网盘(115/189pc/139 起步)的资源通过 `.echo` 占位文件统一管理。

一句话: **输入分享链接 / 挂载路径,输出可被 Emby / OpenList / 媒体工具消费的 `.echo` 文件树,占位极小、由 Echo 按需在云端秒传恢复真实文件。**

### 灵感来源与差异化

| 项目 | 形态 | 云盘范围 | 存储 |
|---|---|---|---|
| RoseHub | 闭源 Docker | 115 单云 | STRM 指向真实云盘文件 |
| NextEmby | 闭源 Python | 115 单云(p115client) | 用户账号秒传转存 |
| **Echo** | **开源 Go (AGPLv3)** | **多云: 115 + 189pc + 139** | **`.echo` 占位 + 按需秒传恢复** |

Echo 给现有生态加上"第三个选择": 完全开源, 多云原生, 单二进制。

### 生态位与依赖关系

```
Echo (本项目, AGPL) ── 资源池业务层
  │ Go module 依赖
  v
openlist-guangyapan-src (AGPL, xmm2022) ── 多云 CAS 扩展 + GuangYaPan driver
  ↑ 派生
OpenList-CAS (AGPL, GitYuA) ── 189pc CAS 模式起源
  ↑ 派生
OpenList (AGPL, OpenListTeam) ── 通用网盘挂载框架
```

Echo 不重写 driver。Echo 作为 Go module 直接 import `openlist-guangyapan-src` 的 `drivers/*` 和 `internal/casmeta`,复用已有的 115/139/189pc CAS 实现以及 GuangYaPan driver。

Echo 自身实现的是上层业务: ingest pipeline、跨云资源池、job 调度、restore API、manifest API、web 后台。

### v0.1 范围

包含:
- Ingest pipeline: 接收 115 / 189pc / 139 分享链接或挂载路径,后台 job 把树批量秒传到目标账号 + 生成 `.echo` 文件
- 跨云资源数据库(files / file_hashes / file_copies)
- 多账号管理(每个 provider 多账号)
- Restore API: 给 file_id / `.echo` 路径,返回当前可用直链
- Manifest API: 列出库内 `.echo` 树及多云副本状态
- 最小 Web 后台(账号 / 库 / Job)
- Auth middleware(v0.1 静态 ADMIN_TOKEN)
- 健康检查 + Prometheus metrics

不包含:
- 用户系统、注册、登录页(预留 `owner_id` 列 + auth 中间件占位; v0.2 替换)
- Emby 反向代理 / `PlaybackInfo` 改写(v0.2)
- TG 爬虫 / TMDB 订阅 / 海报墙(v0.3)
- 跨云副本主动 merge(只记冲突告警,人工处理)
- 真实文件上传(Echo 是 hash-only 服务,实际数据走云端秒传)

### v0.1 之上路线

- **v0.2**: Rose-like Emby 播放代理(消费 Echo manifest + 多用户 Cookie 池)
- **v0.3**: NextFind-like 自动订阅(用 Ingest API + TMDB + TG 抓取)

## 2. 架构与模块

### 部署形态

单进程单二进制:

```
+-----------------------------------------------+
| echo (Go binary, :8080)                       |
|   HTTP API + Web UI                           |
|   Job runner (goroutine pool)                 |
|   SQLite at /data/echo.db                     |
|   .echo output: 本地 fs                       |
|                                                |
|   +-------------------------------------+     |
|   | driverpool (复用 openlist-guangyapan |     |
|   |   通过 Go module import)             |     |
|   |   _115 / _139 / _189pc / guangyapan |     |
|   |   包含已有 CAS 实现                  |     |
|   +-------------------------------------+     |
+-----------------------------------------------+
```

不需要前置 OpenList 进程。不需要任何外部 sidecar。

### Go 包结构

```
echo/
├── cmd/echo/main.go                    入口
├── internal/
│   ├── config/                         env + yaml 加载
│   ├── http/
│   │   ├── server.go
│   │   ├── middleware/auth.go          v0.1 静态 ADMIN_TOKEN
│   │   └── handlers/
│   │       ├── ingest.go
│   │       ├── jobs.go
│   │       ├── manifest.go
│   │       ├── restore.go
│   │       ├── accounts.go
│   │       └── library.go
│   ├── store/                          数据访问层
│   │   ├── schema/                     *.up.sql / *.down.sql
│   │   ├── files.go
│   │   ├── hashes.go
│   │   ├── copies.go
│   │   ├── jobs.go
│   │   ├── accounts.go
│   │   └── libraries.go
│   ├── driverpool/
│   │   ├── driver.go                   统一 facade interface
│   │   ├── pool.go                     account_id → Driver 缓存
│   │   ├── factory.go                  provider → Driver 构造 (走 openlist-guangyapan)
│   │   ├── credentials.go              凭据加密存取
│   │   └── adapter.go                  openlist driver 在 Echo 进程内的生命周期适配
│   ├── echofile/                       .echo 文件 IO
│   │   └── output.go                   写到 local fs
│   │   (Encode/Decode 直接复用 openlist-guangyapan-src/pkg/casmeta)
│   ├── ingest/
│   │   ├── source.go                   Source interface
│   │   ├── share_115.go
│   │   ├── share_189.go
│   │   ├── share_139.go
│   │   ├── path.go                     已挂载路径源 (后置)
│   │   └── pipeline.go                 list → hash → 秒传 → .echo
│   ├── restore/
│   │   ├── resolver.go                 file_id → live copy → 直链
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
├── docker-compose.example.yml          示例: 仅 echo + 反代
├── scripts/
│   ├── dev.sh
│   └── migrate.sh
├── docs/
│   └── superpowers/specs/              本文档
├── integration/                        build tag = integration
└── testdata/                           分享解析 fixtures
```

### 核心 interface

```go
// 统一 driver 接口 — 业务层只依赖这个,不感知具体 provider 实现
type Driver interface {
    Provider() string
    
    // 登录与会话
    Login(ctx context.Context, cred Credentials) (*Session, error)
    Refresh(ctx context.Context) error
    
    // Ingest 路径
    ParseShareLink(ctx context.Context, url string) (*ShareHandle, error)
    ListShare(ctx context.Context, h *ShareHandle) ([]ShareItem, error)
    RapidUpload(ctx context.Context, dst Folder, h FileHashes, name string, size int64) (*RemoteFile, error)
    
    // Restore 路径
    Link(ctx context.Context, ref FileRef) (*DirectLink, error)
    
    // 维护
    Delete(ctx context.Context, ref FileRef) error
    Mkdir(ctx context.Context, parent Folder, name string) (Folder, error)
}

// Source: ingest 输入抽象
type Source interface {
    Provider() string
    List(ctx context.Context) ([]SourceItem, error)
}

type SourceItem struct {
    RelPath string
    Size    int64
    Hashes  map[string]string   // {"sha1":"...", "md5":"...", ...}
    Native  any                  // 原生 ref (pickcode / shareCode / etc.)
}

// 输出 .echo 文件
type Output interface {
    Mkdir(ctx context.Context, path string) error
    Put(ctx context.Context, path string, data []byte) error
    List(ctx context.Context, path string) ([]string, error)
}
// v0.1 提供 LocalOutput 实现; WebDAV / S3 实现挂 v0.2+
```

### Driver 实现策略 (Go module 复用)

`go.mod`:

```
require (
    github.com/xmm2022/openlist-guangyapan-src v0.x.y
)
```

工作流:

1. openlist-guangyapan-src 仓库继续维护 driver + CAS 代码 (AGPL)
2. 重大节点打 git tag (v0.1.0 / v0.1.1 / ...)
3. Echo 用 `go get github.com/xmm2022/openlist-guangyapan-src@v0.1.x` 升版
4. 本地协同开发可在 Echo 的 `go.mod` 加 `replace github.com/xmm2022/openlist-guangyapan-src => ../openlist-guangyapan-src`

`driverpool/factory.go` 按 provider 构造 driver:

- 115 → `drivers/115` 或 `drivers/115_open` (按账号凭据类型选)
- 139 → `drivers/139` (`personal_new` 模式,含 SHA256 CAS)
- 189pc → `drivers/189pc` (家庭传输 + CAS 套件,源自 OpenList-CAS,你 fork 已扩展)
- guangyapan → `drivers/guangyapan` (光鸭网盘)

`driverpool/adapter.go` 处理 openlist driver 在 Echo 进程内的生命周期:

- 提供轻量 `model.Storage` 实例承载凭据 + 配置
- 旁路 OpenList 自身的 storage manager (`internal/op`)
- 接管 token 刷新 (Echo 用 lazy refresh; OpenList 用定时器)
- 预估代码 200-300 行,一次性写完

### internal/casmeta 的可见性

`internal/casmeta` 在 Go module 规则下默认不能被外部 import。需要在 openlist-guangyapan-src 做一次小重构:

把 `internal/casmeta/casmeta.go` 提到 `pkg/casmeta/casmeta.go`。一次性操作。

Echo 通过 `github.com/xmm2022/openlist-guangyapan-src/pkg/casmeta` 直接复用 Payload 结构、Encode/Decode、HasherWriter,无需移植。

## 3. 数据模型

### DDL (SQLite,PG 兼容)

```sql
-- 账号池(多云,多账号)
CREATE TABLE accounts (
  id            TEXT PRIMARY KEY,                  -- 用户起的名字: "115-main"
  provider      TEXT NOT NULL,                     -- 115 / 139 / 189pc
  credentials   TEXT NOT NULL,                     -- JSON,敏感字段 AES-GCM 加密
  status        TEXT NOT NULL,                     -- ok / token_expired / banned
  last_check    INTEGER,
  owner_id      TEXT NOT NULL DEFAULT 'admin',
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

-- 资源库
CREATE TABLE libraries (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  name            TEXT NOT NULL,
  echo_output_kind TEXT NOT NULL,                  -- v0.1 仅支持 "local"; v0.2+ 加 "webdav" / "s3"
  echo_output_path TEXT NOT NULL,                  -- 本地路径,例如 /data/output/lib1
  owner_id        TEXT NOT NULL DEFAULT 'admin',
  created_at      INTEGER NOT NULL
);

-- 资源指纹(一个文件 = 一行,跨云去重的核心)
CREATE TABLE files (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  size          INTEGER NOT NULL,
  name          TEXT NOT NULL,
  library_id    INTEGER NOT NULL REFERENCES libraries(id),
  rel_path      TEXT NOT NULL,                     -- 库内相对路径
  source_mtime  INTEGER,                           -- 原始 mtime(可选)
  owner_id      TEXT NOT NULL DEFAULT 'admin',
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  UNIQUE (library_id, rel_path)
);
CREATE INDEX idx_files_library ON files(library_id);

-- 多 hash 关联(支持跨 hash 查同一文件)
CREATE TABLE file_hashes (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  file_id       INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  hash_type     TEXT NOT NULL,                     -- md5/sha1/sha256/preid/slice_md5
  hash_value    TEXT NOT NULL,
  UNIQUE (hash_type, hash_value)                   -- 全局唯一,自动跨云去重
);
CREATE INDEX idx_file_hashes_file ON file_hashes(file_id);

-- 多云副本
CREATE TABLE file_copies (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  file_id       INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  provider      TEXT NOT NULL,
  account_id    TEXT NOT NULL REFERENCES accounts(id),
  cloud_file_id TEXT,                              -- 云盘内 ID
  pickcode      TEXT,                              -- 115 专用
  status        TEXT NOT NULL CHECK (status IN ('live','dead','pending')),
  last_seen     INTEGER NOT NULL,
  UNIQUE (provider, account_id, cloud_file_id)
);
CREATE INDEX idx_file_copies_live ON file_copies(file_id, status, last_seen DESC);

-- Job 队列
CREATE TABLE jobs (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  kind          TEXT NOT NULL,                     -- ingest / restore / verify
  status        TEXT NOT NULL,                     -- pending/running/done/failed/cancelled
  payload       TEXT NOT NULL,                     -- JSON
  progress      TEXT,                              -- JSON {current,total,msg,warnings:[]}
  error         TEXT,
  owner_id      TEXT NOT NULL DEFAULT 'admin',
  created_at    INTEGER NOT NULL,
  started_at    INTEGER,
  finished_at   INTEGER
);
CREATE INDEX idx_jobs_status ON jobs(status, created_at);
```

### `.echo` 文件格式

`.echo` 直接复用 openlist-guangyapan-src 既有的 `casmeta` 实现 (`pkg/casmeta`,需要做一次 internal → pkg 移动)。新文件扩展名为 `.echo`,但内容格式与 OpenList-CAS / openlist-guangyapan-src 输出的 `.cas` 完全相同 (base64 JSON,字段: name / size / md5 / sliceMd5 / sha256 / create_time)。

向后兼容: Echo 同时识别 `.echo` 和 `.cas` 扩展名。让你 fork 里既有的 `.cas` 库可以直接被 Echo 消费,不需要批量改后缀。新生成的占位用 `.echo` 扩展。

未来如需扩展格式(加 sha1 / preid 等字段),在 `pkg/casmeta` 主仓库统一推进,Echo 升 module 版本即生效。

### v0.1 不做的: file_id merge

如果同一物理文件先后以 sha1 和 md5 进入系统,会变成两个不同的 file_id:

- 时刻 T1: 115 分享 ingest → 创建 file(id=1) + file_hashes(sha1=X)
- 时刻 T2: 189 ingest 同一物理文件 → 因 sha1 未在 file_hashes 命中(189 给 md5),创建 file(id=2) + file_hashes(md5=Y)

后来发现 file_id=1 和 file_id=2 其实是同一物理文件(比如某次 ingest 上报了 (sha1, md5) 同时), v0.1 **不自动 merge**,而是记 conflict 警告日志到 stderr + admin UI 提示。理由:

- merge 涉及合并 file_copies、保留较老 file_id、改 .echo 文件等,逻辑复杂
- 实际触发频率不可知,过早实现可能根本用不上
- 等真用上时再加 merge 工具

## 4. 数据流

### Ingest

```
POST /api/ingest
Authorization: Bearer <ADMIN_TOKEN>
{
  "library_id": 1,
  "source": {
    "type": "share_link",
    "provider": "115",
    "url": "https://115.com/s/xxx?password=abc"
  },
  "target_account": "115-main"
}
         |
         v
[handler]
  - 插入 jobs(kind=ingest, status=pending, payload=...)
  - 返回 {"job_id": 42}
         |
         v
[job runner pick up, status=running]
         |
         v
[ingest.pipeline.Run]
  1. source.List() — 用 driver.ParseShareLink + ListShare
     返回 [{rel_path, size, hashes{sha1,...}, native}, ...]
  
  2. for each item (并发度 from config):
     a. 在 file_hashes 查任一 hash 是否命中 → file_id 复用 / 新建
     b. 写齐所有已知 hash 到 file_hashes
     c. driver.RapidUpload(target_account, hashes, name, size)
        - 成功 → file_copies(status=live, last_seen=now)
        - 失败 → job.warnings += 1, 跳过这个文件(不阻塞)
     d. echofile.Writer.Encode(file_id) → output.Put(rel_path + ".echo", bytes)
  
  3. jobs.progress 实时更新(1s 节流)
         |
         v
完成: jobs.status=done(可能 warnings>0)
失败: jobs.status=failed, error=stack
```

### Restore

```
GET /api/restore/{file_id}?prefer=115
         |
         v
[handler]
  1. 查 file_copies WHERE file_id=? AND status='live'
     ORDER BY (provider=prefer DESC), last_seen DESC LIMIT 5
  
  2. 逐个尝试 driver.Link(account, cloud_file_id/pickcode):
     - 200 → 302 redirect (含必要 cookie / UA)
     - 404/410/403 → file_copies.status=dead, 试下一个
     - 5xx / 网络 → 不改 status,试下一个,记 warning
  
  3. 全部失败:
     - v0.1: 返回 404 + JSON {dead_copies:[...]}
     - v0.2 可选: 触发 restore-on-demand job 从其他副本秒传到 prefer
         |
         v
缓存(memory, TTL=60s):
  key = (file_id, account_id) → DirectLink
  避免 Emby 播放窗口内重复打 driver
```

也可走 `GET /api/restore/by-echo?path=...`,handler 先用 `echofile.Reader` 解码 `.echo` 文件 → 拿 hashes → 反查 file_id → 走上述流程。

### 关键决策

| 决策 | 选择 | 理由 |
|---|---|---|
| 秒传失败 | 跳过该文件 + 记 warning,不阻塞 job | 跨云资源会陆续到位,单点失败不该回滚整个 ingest |
| 同 hash 跨账号去重 | 多行 file_copies 共享同一 file_id | 自然反映"一个文件,多云副本" |
| 跨 hash merge | v0.1 不做,只记 conflict | 复杂逻辑,等真实数据触发再实现 |
| `.echo` 写盘位置 | library 级配置,v0.1 仅 local fs | 简化首版;v0.2 加 WebDAV(Emby 远程扫描场景) |
| 直链缓存 | memory TTL=60s | 性能优化 |
| Job 进度持久化 | SQLite, 1s 节流写 | 重启可见,但 running job 不自动续(标 failed) |
| Job 并发 | 单 job 内 N worker(默认 4) + 多 job 排队(默认 4 并发) | 简单可调,环境变量覆盖 |

## 5. 错误处理与降级

### 分层错误类型

```go
// store 层
var (
    ErrNotFound    = errors.New("not found")
    ErrConstraint  = errors.New("constraint violation")
)

// driverpool 层  
var (
    ErrAccountUnknown      = errors.New("account not registered")
    ErrTokenExpired        = errors.New("token expired")
    ErrProviderUnsupported = errors.New("provider not supported")
    ErrRapidUploadDenied   = errors.New("rapid upload not permitted")
)

// ingest / restore 层
var (
    ErrShareParseInvalid = errors.New("share link invalid or expired")
    ErrAllCopiesDead     = errors.New("no live copy available")
)
```

handler 层映射到 HTTP:
- ErrNotFound → 404
- ErrTokenExpired → 503 + admin 提示
- ErrShareParseInvalid → 400
- 其余 → 500 + 日志

### 降级策略

| 场景 | 策略 |
|---|---|
| Job 中单文件秒传失败 | 跳过 + warning,job 整体 done(warnings>0) |
| Restore 单 copy 失败 | 标 dead,自动 fallback,全死返 404 |
| Driver token 过期 | 触发同步 refresh,重试一次;仍失败 → 标 account.status=token_expired |
| `.echo` 输出失败(disk full / WebDAV 401 等) | Job 直接 failed(admin 配置问题不该静默) |
| 进程崩溃 | 启动时把 running job 标 failed + error="interrupted",人工重跑 |

### 不做的

- 不做自动 retry-with-backoff(失败给人看清比假装成功重要)
- 不做 circuit breaker(account.status 就是手工断路器)
- v0.1 不做跨进程分布式锁(单实例假设)

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

crypto:
  master_key: ${ECHO_MASTER_KEY}        # 32 字节 base64

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

环境变量覆盖任何 yaml 字段(`ECHO_SERVER_BIND` 等)。

### 凭据存储

`accounts.credentials` 是 JSON,但每个敏感字段(cookie / refresh_token / password)用 AES-GCM 单独加密,key 来自 `ECHO_MASTER_KEY`。dump 数据库不直接泄露凭据。

样例(加密前):

```json
{
  "type": "cookie",
  "cookie": "UID=xxx;CID=yyy;SEID=zzz",
  "user_agent": "Mozilla/5.0..."
}
```

存储时变成:

```json
{
  "type": "cookie",
  "cookie": "enc:v1:<nonce>:<ciphertext>:<tag>",
  "user_agent": "Mozilla/5.0..."
}
```

## 7. 测试策略

### 单元测试(CI 必跑)

| 包 | 测试重点 |
|---|---|
| `store/*` | DDL migration、CRUD、UNIQUE 触发、级联删除、并发安全 |
| `ingest/source_*` | fixture HTML / JSON 喂分享解析,验证 List 输出 |
| `ingest/pipeline` | mock driver + mock store,验证 hash 命中/未命中/秒传失败 各分支 |
| `echofile/{writer,reader}` | encode/decode round-trip,与 OpenList CAS 输出互通验证 |
| `restore/resolver` | mock driver,验证 copy fallback / 全死 |
| `driverpool/adapter` | mock openlist driver 接口,验证生命周期适配 + token 刷新触发 |
| `driverpool/factory` | provider name → 正确的 openlist driver 类型构造 |

### 集成测试(build tag `integration`,CI 可选)

| 测试 | 描述 |
|---|---|
| Real 115 ingest | 配真账号 + 测试分享 → 跑完整 pipeline → 校验 DB + .echo |
| Real Restore | 已 ingest 的 file_id → 调 Restore → 验证 302 直链可下载 |
| Multi-job concurrency | 10 个 ingest job 并发,验证数据隔离与并发上限 |

集成测试本地手跑:`go test -tags=integration ./integration/...`。CI nightly。

### 测试工具

- `testify/assert` + `testify/mock`
- `httptest` 模拟 driver HTTP 响应
- goldenfile: 分享解析 expected output 存 `testdata/*.golden`

## 8. 监控与运维

### 内置 endpoint

| Path | 用途 |
|---|---|
| `GET /healthz` | liveness,只看进程存活 |
| `GET /readyz` | readiness,检查 DB + 至少一个 account 可达 |
| `GET /metrics` | Prometheus 格式 |

### Metrics 埋点

```
echo_jobs_total{kind, status}                        counter
echo_ingest_files_total{provider, result}            counter  
   result: success / rapid_fail / parse_error / driver_error
echo_restore_requests_total{provider, result}        counter
echo_restore_latency_seconds{provider}               histogram
echo_driver_calls_total{provider, method, status}    counter
echo_driver_call_latency_seconds{provider, method}   histogram
echo_db_open_connections                             gauge
echo_account_status{provider, account_id}            gauge  (ok=1 expired=0 banned=-1)
```

### 日志

- `log/slog`(Go 1.21+ 标准库)
- 默认 JSON 输出到 stdout(docker 友好)
- 配置可切 text 格式(开发场景)
- 关键事件: account 状态变化、job 状态变化、跨 hash conflict 警告

## 9. 部署

### docker-compose.example.yml

```yaml
services:
  echo:
    image: ghcr.io/xmm2022/echo:latest
    ports:
      - "8080:8080"
    environment:
      ECHO_ADMIN_TOKEN: ${ECHO_ADMIN_TOKEN}
      ECHO_MASTER_KEY: ${ECHO_MASTER_KEY}
    volumes:
      - ./data:/data
      - ./output:/data/output           # .echo 文件输出
    restart: unless-stopped

  # 可选: nginx/caddy 反代
```

### 反代(nginx 示例)

```nginx
location /echo/ {
  proxy_pass http://echo:8080/;
  proxy_set_header Host $host;
  proxy_set_header X-Real-IP $remote_addr;
  client_max_body_size 0;               # 允许 ingest 时的大 payload
}
```

### 备份

- SQLite 文件(`/data/echo.db`)
- `.echo` 输出目录
- master_key(独立保管,丢失后无法解密 credentials)

### 单实例上限

- SQLite 推荐 ≤ 10 万文件 / 10 万 copies; 更大量级切 PostgreSQL
- 单进程: 默认 4 并发 job × 4 worker = 16 个并发秒传调用
- 多实例不支持(v0.1 假设单实例,无分布式锁)

## 10. License

AGPLv3。详见 `LICENSE`。

依据 AGPL: 通过网络提供 Echo 服务即等同于"分发",必须向用户提供完整源码 (或派生版本源码)。商业服务、自部署均受此约束。

### 选择 AGPL 的理由

1. **生态对齐**: OpenList、OpenList-CAS、openlist-guangyapan-src 全部 AGPL。Echo 作为 Go module 依赖 openlist-guangyapan-src,自动受 AGPL 传染——选择主动认领 AGPL 比起隐式继承更清晰。
2. **保证"第三选择"永久免费**: copyleft 让任何 fork / SaaS 衍生品都必须开源,防止后续被任意闭源化。
3. **明确边界**: 跟闭源的 NextEmby / RoseHub 形成 license 层面的对比。

### License 链条 (向上游)

| 项目 | License | 角色 |
|---|---|---|
| Echo (本项目) | AGPLv3 | 资源池业务层 |
| openlist-guangyapan-src (xmm2022) | AGPLv3 | 多云 CAS + GuangYaPan driver |
| OpenList-CAS (GitYuA) | AGPLv3 | 189pc CAS 模式起源 |
| OpenList (OpenListTeam) | AGPLv3 | 通用网盘挂载框架 |

致谢见 `README.md`。AGPL 第 5 节要求保留版权声明,Echo 在以下位置承担:

- `LICENSE` 文件 (AGPLv3 全文)
- `README.md` 致谢章节
- `pkg/casmeta` 复用代码保留原作者署名 (你本人)
- 任何上游 fork 关系在源码注释中标明

### 第三方 Go module 间接 license

通过 openlist-guangyapan-src 间接引入的 SDK:

- `SheltonZhu/115driver` — MIT (兼容 AGPL)
- `OpenListTeam/115-sdk-go` — Apache 2.0 (兼容 AGPL)
- `go-resty/resty/v2` — MIT
- 其他通用 Go 工具库 — 多为 MIT/Apache

所有间接依赖与 AGPL 兼容。

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

- 项目名: **Echo**
- 文件格式: **`.echo`** (同时识别旧 `.cas`,内容格式完全相同)
- 语言: **Go 1.22+**
- License: **AGPLv3** (跟 OpenList / OpenList-CAS / openlist-guangyapan-src 生态对齐)
- 部署: 单 Go 二进制,docker-compose 单服务
- 数据库: SQLite(v0.1) → PostgreSQL(后期可选)
- HTTP 框架: `chi`(轻量、标准库友好)
- 模板: `templ`(Go 类型安全模板)+ htmx 渐进增强
- 日志: `log/slog`
- 数据库访问: `sqlc`(代码生成,类型安全)
- 迁移: `golang-migrate`
- 用户系统: v0.1 不做,`owner_id` 列预留 + auth middleware 占位
- 资源指纹主键: 自增 ID + (hash_type, hash_value) 联合 UNIQUE 索引
- 多账号秒传: v0.1 单账号/job; 多账号 = 多 job(UI 一键创建 N 个)
- 跨 hash merge: v0.1 不主动 merge,记 conflict 警告
- 跨云副本选择: prefer 偏好 + last_seen DESC
- **driver 实现**: Go module import `github.com/xmm2022/openlist-guangyapan-src`,复用 115 / 139 / 189pc / guangyapan driver + casmeta
- **`pkg/casmeta` 重构**: openlist-guangyapan-src 把 `internal/casmeta` 提到 `pkg/casmeta` (一次性小重构,让外部包可 import)
- **致谢**: README 标明 OpenList → OpenList-CAS → openlist-guangyapan-src → Echo 的派生关系

## 13. v0.1 不解决的问题(留给后续版本)

- 多用户 / 多租户(v0.2)
- 用户配额追踪(v0.2)
- Emby 集成(v0.2)
- 资源订阅 / 自动找回(v0.3)
- 跨实例分布式(v1.0+)
- file_id 自动 merge 工具(按需,触发后再做)
- 阿里云盘 / 百度网盘 / 夸克 等新 provider(等社区秒传协议成熟)
