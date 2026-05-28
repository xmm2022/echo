# Echo

跨云盘资源池服务。把多家网盘（115、189pc、139 起步）的资源通过 `.echo` 占位文件统一管理：丢一个分享链接进来，自动在指定账号上秒传一份，输出可被 Emby/OpenList/媒体工具消费的 `.echo` 文件树。

> 状态：v0.1 设计阶段。设计文档见 [`docs/superpowers/specs/2026-05-28-echo-design.md`](docs/superpowers/specs/2026-05-28-echo-design.md)。

## 核心概念

- **Echo 文件 (`.echo`)**：极小的占位文件（几百字节），保存原始文件的名字、大小、多种 hash（md5 / sliceMd5 / sha1 / sha256 / preid）。访问 `.echo` 时由 Echo 调用云端秒传接口在指定账号下实例化真实文件，再返回直链。
- **跨云去重**：同一个文件的 hash 一旦在某个云盘出现，Echo 会记录其副本；其他云盘后续出现同一 hash 时自动关联到同一逻辑文件。
- **多账号资源池**：单个 provider 下可挂多个账号，秒传/恢复时按策略选账号。

## 设计目标

- 多云原生（不依赖任何单一云盘的特殊机制）
- 单 Go 二进制部署
- MIT，未来可选闭源
- 与现有 OpenList / Emby / RoseHub / NextEmby 生态可互操作

## 状态

设计阶段。代码尚未编写。

## License

MIT。详见 [`LICENSE`](LICENSE)。
