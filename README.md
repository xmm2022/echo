# Echo

跨云盘资源池服务。把多家网盘（115、189pc、139 起步）的资源通过 `.echo` 占位文件统一管理：ingest 阶段在指定账号上秒传一份并落库，输出可被 Emby / OpenList / 媒体工具消费的 `.echo` 文件树；访问 `.echo` 时由 Echo 通过 sidecar 拿直链或代理流式回放，不再二次实例化。v0.1 中只有 115 支持"丢一个分享链接进来自动 ingest"（`115share2cas` 自动 exec）；139 走 manual import（用户自行跑 `cas139` 生成 `.cas` tree 后通过 `/api/ingest/manual` 导入）。

> 状态：v0.1 设计阶段。设计文档见 [`docs/superpowers/specs/2026-05-28-echo-design.md`](docs/superpowers/specs/2026-05-28-echo-design.md)。

## 定位

Echo 是 NextEmby（闭源 PyArmor）和 RoseHub（闭源 Docker）之外的**第三个选择**：

- 完全开源（AGPLv3）
- 多云盘原生（115 / 189pc / 139，未来更多）
- 单 Go 二进制部署

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

## 致谢

- [OpenList](https://github.com/OpenListTeam/OpenList) — 提供多云盘抽象与驱动框架
- [OpenList-CAS](https://github.com/GitYuA/OpenList-CAS) — 首创 `.cas` 占位文件 + 189pc 秒传恢复模式
- [openlist-guangyapan-src](https://github.com/xmm2022/openlist-guangyapan-src) — 把 CAS 扩展到 115 / 139 / GuangYaPan

## License

AGPLv3。详见 [`LICENSE`](LICENSE)。

依据 AGPLv3 §13：网络交互本身**不**等同于 GPL 意义上的 convey / distribution，部署未经修改的 Echo 不触发 §13 的额外源码提供义务（但仍受 §4–§6 约束：保留版权声明、衍生作品继续 AGPLv3 等）。若部署的是经过修改的 Echo 版本并对远程用户提供网络服务，则必须向这些用户提供该修改版的 Corresponding Source。完整解读见设计文档 §10。
