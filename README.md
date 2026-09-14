# gptissobad —— 给中文用户用的 Codex 本机翻译网关（Linux 版）

> **作者没有 CODEX 可用，本项目由万恶的 MUSE SPARK 1.3 开发完成。**
>
> 本项目主要是为了解决 GPT 访问繁忙时不好用的问题：把中文请求先翻成英文再
> 发给上游，并让回答保持中文，中间任何一环出问题都不掐断对话。
> **不保证对任何一个人有效**——上游模型、翻译服务、网络随时会变，能不能用、
> 好不好用，自己实测为准。
> 这是 **LINUX 版**，其他操作系统请把代码拉下去，用 AI 改编成自己系统的版本。

## 它解决什么

1. **中文不好使**：直接发中文经常翻车（被拒、被限流、答非所问）。网关把你的
   中文翻成英文再发给 GPT，GPT 回英文，网关让你看到中文回答。
2. **上游繁忙时不断连**：翻译失败、超时、上游抖动，网关把原文直接送过去，
   最多回英文，不掐你对话。只有断网、断粮（翻译额度用完）才回 403。
3. **防忘开服务**：前置轻进程常驻 Codex 指向的端口，网关没起时对话直接 403
   （而不是连接被拒），配合开机自启和 Codex 插件兜底。

## 原理（详细）

请求链路（全部只走本机回环 `127.0.0.1`，没有局域网/公网监听）：

```text
Codex ──▶ 前置 :18787 ──▶ 真网关 :18788 ──▶ 上游（你的 GPT 账号）
              │                 │
              │                 ├──▶ 翻译服务（见下）
              │                 └──▶ 转发/直放
              └──▶ 网关没起：直接 403
```

三个二进制（`go build` 出来即用）：

- `codex-watchdog`：Control Page（`http://127.0.0.1:18786/`）+ 真网关 + 可选托盘图标。
  托盘图标绿=正常、红=停止或故障、翻译中闪烁，点按打开 Control Page。
- `codex-fronthost`：前置轻进程。只做透传：后端不在就 403（`网关未就绪`），
  后端的 403 原样透传，WebSocket 一律拒绝。它不翻译、不记正文、不花 token。
- `codex-translate`：无界面版网关（环境变量配置），适合手动跑。

### 翻译是怎么发生的

1. 只有 POST 请求里 `role=user` / `role=assistant` 的文本进翻译；路径、
   文档、工具输出、指令、查询串、请求头里的中文**不翻译、原样放行**。
2. 中文路径（如中文工作目录）先摘出来占位，翻完再填回去，不送翻译器。
3. 主翻译用 MIMO 系模型（`temperature: 0` + “只输出译文、永不作答”指令）；
   主模型回中文（交白卷）则自动用备用模型按 Responses 方言重试一次，还不行
   才 403。密钥行永远不进翻译器。
4. 首条用户文本之前统一前置 `Please reply in Chinese.`，中英问法都回中文。
5. 译文按内容哈希缓存（含内存 + 本机文件 `~/.codex/translate-cache.json`），
   相同的话只花一次钱；增量会话只翻新增部分。

### 翻译服务与 Key（自备，仓库里没有任何 key）

翻译要调第三方接口，**服务自己挑便宜的用，key 自己准备，费用走自己的账**，
本仓库不带、不收、不要任何人的 key。我用的是小米的 token 专线，便宜量大；
更便宜的甚至免费的一大把，中翻英这种活一般的 LLM 都干得了，自己比价就是。

- `mimo*` 模型默认走 `https://token-plan-cn.xiaomimimo.com/v1`，key 放
  `~/.codex/mimo.key`（0600）或 `MIMO_API_KEY` 环境变量；
- 其他模型走 OpenCode Go（`https://opencode.ai/zen/go/v1/chat/completions`），
  key 顺序：`~/.codex/opencode-go.key` → `OPENCODE_API_KEY`；
- 备用翻译默认 `muse-spark-1.3-contributor`（Control Page 可改，填 `-` 关闭）。

### 403 穷举（只这些情况，其他一律放行）

1. 翻译额度用尽（429/402，正文点名“额度”）；
2. 翻译器故障（超时、5xx、4xx、非法返回；主备都回中文）；
3. 翻译服务连接不上。
   另有传输层例外：WebSocket、上游建连/首字节超时。其他任何中文场景都放行。

### Control Page 能看到什么

翻译请求数、缓存命中、已拒绝、增删改查的增量/全量、累计翻译字符、
Prompt/Completion/Total Tokens、备用翻译次数。只记数不记正文不记 key。

## 构建与运行（Linux）

需要 Go 1.22+：

```sh
go test ./...
go build -o codex-watchdog ./cmd/codex-watchdog
go build -o codex-fronthost ./cmd/codex-fronthost
go build -o codex-translate ./cmd/codex-translate
./codex-watchdog --no-tray
```

打开 `http://127.0.0.1:18786/`：填上游 Base URL（Cockpit 类用 `/v1`，
直连 ChatGPT 用 `/backend-api/codex`）、网关端口、翻译模型，打开开关，
把 TOML 贴进用户级 `~/.codex/config.toml`：

```toml
model_provider = "codex_translate"

[model_providers.codex_translate]
name = "Codex translate"
base_url = "http://127.0.0.1:18787/v1"
wire_api = "responses"
supports_websockets = false
env_key = "CODEX_TRANSLATE_KEY"
```

一键装插件与自启（含 Codex `SessionStart` 自动拉起）：

```sh
./scripts/install-plugin.sh
```

## 目录结构

- `cmd/`：三个二进制入口；
- `internal/gateway/`：翻译、转发、缓存、计量；
- `internal/fronthost/`：前置轻进程；
- `internal/control/`：Control Page 与配置（`frontend/` 为页面）；
- `internal/opencodego/`：OpenCode 凭据约定；
- `internal/tray/`：Linux 托盘图标（SNI）；
- `docs/spec/`：设计文档（最高策略见 `translation-policy.md`）；
- `docs/research/`：调研笔记；
- `pack/`：Codex 插件与打包文件；`scripts/`：安装脚本。

## 免责与说明

- 出生原因就是 GPT 繁忙难用，但**不承诺解决任何人的繁忙问题**，也不承诺
  任何效果；模型与上游策略随时会变，以你实测为准；
- 仅在 Linux 验证过（Mint/Xfce托盘），Windows/macOS 请自行用 AI 改编，
  托盘、自启、沙箱相关代码都要动；
- 你的中文原文会发给**你自己配置**的翻译服务（ unavoidable，做翻译就得给人看），
  介意者勿用；
- Issue 欢迎带 Control Page 日志行与指标截图（不要贴 key 和正文）。
