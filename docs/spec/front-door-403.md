# 前置轻 Host 与 403 契约 Spec

## 1. 目标

Codex 永远只打前置（默认 `127.0.0.1:18787`）。前置先启动、常驻、本机回环；
真网关没起时 Codex 对话只能收到 HTTP 403，杜绝两种更坏的结果：连接被拒
（重试风暴、提示混乱）和未经网关直连上游（限流风险自担，见 translation-policy.md）。

## 2. 范围与边界

前置只做三件事：`GET /healthz`、`GET` 模型目录、原样透传其他请求。
它不做翻译、不读密钥、不记正文、不调 Translator Backend（不产生费用）。

- 前置与真网关之间只走本机回环；非回环 backend 直接拒绝启动；
- WebSocket 一律 403（翻译看不见的传输不能当逃生口）；
- 真网关自身的 403（额度、翻译器故障、连不上）原样透传，不改写；
- 只有建连失败（真网关没在听）才由前置回 `网关未就绪，这一轮未发送` + 403。

## 3. 健康定义（分层）

- 前置活着 = 端口通（`/healthz` 200）。Codex 能启动、能列模型（模型目录在
  后端 Down 时回最小 `models`+`data` 双字段，Codex 用 fallback 元数据继续）。
- 服务全好 = 前置通 + 真网关 Control Page `running` + 翻译测试通过。
- 任何一层不好，Codex turn 的结果都是 403，只是 message 不同以便定位：
  前置 403 看 `ensure.log` 和 Control Page，后端 403 看网关日志与指标。

## 4. 端口分工

- 前置（Codex-facing）：默认 `18787`，TOML 和 listen-line 显示它；
- 真网关（内部）：开前置后改到 `18788` 这类内端口，只接受前置转来的连接；
- 关前置时一切回到从前：Codex 直连真网关端口，TOML 自动切回。

## 5. 插件契约（SessionStart）

- 只做端口探测 + 拉进程，8 秒等不到就静默退出（exit 0，不阻塞开会话）；
- 成功不输出（不污染上下文），失败只写 `~/.codex/logs/ensure.log`；
- 每开会话都拉前置和 Watchdog（POST `/api/front` 打开前置）；关前置挡不住下一轮 SessionStart；
- 并发多会话同时触发安全：抢绑失败的一方退出，赢的一方服务大家；
- 改 hook 文件后必须去 `/hooks` 重 trust；`allow_managed_hooks_only` 会跳过它。

## 6. 验收标准

- 真网关停掉时，`POST` 任意 turn 路径回 403 + 前置 message，不转发 body；
- 真网关在时，`POST` 原样透传（含它自己的 200/403），`GET` 模型目录透传；
- `/healthz` 永远 200；WebSocket 永远 403；
- 非回环 backend 拒绝启动；
- Control Page 能看到前置开关、端口、是否可达，TOML 永远指向 Codex 该打的地址。
