# Echo

跨云盘资源池服务。把多家网盘（115、189pc、139 起步）的资源通过 `.echo` 占位文件统一管理：丢一个分享链接进来，自动在指定账号上秒传一份，输出可被 Emby/OpenList/媒体工具消费的 `.echo` 文件树。

> 状态：v0.1 设计阶段。设计文档见 [`docs/superpowers/specs/2026-05-28-echo-design.md`](docs/superpowers/specs/2026-05-28-echo-design.md)。

## 定位

Echo 是 NextEmby（闭源 PyArmor）和 RoseHub（闭源 Docker）之外的**第三个选择**：

- 完全开源（AGPLv3）
- 多云盘原生（115 / 189pc / 139，未来更多）
- 单 Go 二进制部署

## 核心概念

- **Echo 文件 (`.echo`)**：极小的占位文件（几百字节），保存原始文件的名字、大小、多种 hash（md5 / sliceMd5 / sha1 / sha256 / preid）。访问 `.echo` 时由 Echo 调用云端秒传接口在指定账号下实例化真实文件，再返回直链。
- **跨云去重**：同一个文件的 hash 一旦在某个云盘出现，Echo 会记录其副本；其他云盘后续出现同一 hash 时自动关联到同一逻辑文件。
- **多账号资源池**：单个 provider 下可挂多个账号，秒传/恢复时按策略选账号。

## 生态位

```
Echo (本项目, AGPL) — 资源池业务层
  ↓ Go module 依赖
openlist-guangyapan-src (AGPL, xmm2022) — 多云 CAS 扩展 + GuangYaPan driver
  ↑ 派生
OpenList-CAS (AGPL, GitYuA) — 189pc CAS 起源
  ↑ 派生
OpenList (AGPL, OpenListTeam) — 通用网盘挂载框架
```

Echo 不重写 driver，而是站在已有 CAS driver 之上做业务编排（ingest / restore / 资源池 / Job）。

## 致谢

- [OpenList](https://github.com/OpenListTeam/OpenList) — 提供多云盘抽象与驱动框架
- [OpenList-CAS](https://github.com/GitYuA/OpenList-CAS) — 首创 `.cas` 占位文件 + 189pc 秒传恢复模式
- [openlist-guangyapan-src](https://github.com/xmm2022/openlist-guangyapan-src) — 把 CAS 扩展到 115 / 139 / GuangYaPan

## License

AGPLv3。详见 [`LICENSE`](LICENSE)。

依据 AGPL：通过网络提供 Echo 服务即等同于"分发"，必须向用户提供完整源码（或 Echo 派生版本源码）。商业服务、自部署均受此约束。
