# 本项目更像一个安慰剂，很可能屁用没有

# gptissobad —— 给中文用户用的 Codex 本机翻译网关（Linux / Windows / macOS）

> **作者没有 CODEX 可用，本项目由万恶的 MUSE SPARK 1.3 开发完成。**
>
> 本项目主要是为了解决 GPT 访问繁忙时不好用的问题：把中文请求先翻成英文再
> 发给上游，并让回答保持中文，中间任何一环出问题都不掐断对话。
> **不保证对任何一个人有效**——上游模型、翻译服务、网络随时会变，能不能用、
> 好不好用，自己实测为准。
> 平台支持：**Linux 全功能**（Mint/Xfce 托盘已实测）；**Windows 已实现**——原生
> 通知区图标、自带 `.ico` 渲染、Control Page 与前置进程都走同一套纯 Go 代码，代码层面
> 已通过 `GOOS=windows` 的编译与 `go vet`（三个架构），但作者没有 Windows 机器实测；
> **macOS 可构建可运行**，菜单栏图标暂缺（不引入 cgo 就到不了 Objective-C 运行时），
> 用 Control Page 代替，详见下面「构建与运行」。

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
  托盘图标绿=正常、红=停止或故障、翻译中闪烁，点按打开 Control Page；Linux 走 SNI，
  Windows 走通知区（无 godbus 依赖），macOS 目前没有图标、只留 Control Page。
- `codex-fronthost`：前置轻进程。只做透传：后端不在就 403（`网关未就绪`），
  后端的 403 原样透传，WebSocket 一律拒绝。它不翻译、不记正文、不花 token。
- `codex-translate`：无界面版网关（环境变量配置），适合手动跑。

### 翻译是怎么发生的

先说死、免误会：**翻译是单向的**——只把你的中文翻成英文给上游看。
上游回什么就是什么，网关**不做**英→中回译（回来的英文你要是看不懂，
那是上游模型的事，不是网关偷懒）。

1. 只有 POST 请求里 `role=user` / `role=assistant` 的文本进翻译；路径、
   文档、工具输出、指令、查询串、请求头里的中文**不翻译、原样放行**。
2. 中文路径（如中文工作目录）先摘出来占位，翻完再填回去，不送翻译器。
3. 主翻译用 MIMO 系模型（`temperature: 0` + “只输出译文、永不作答”指令）；
   主模型回中文（交白卷）则自动用备用模型按 Responses 方言重试一次，还不行
   才 403。密钥行永远不进翻译器。
4. 首条用户文本之前统一前置 `Please reply in Chinese.`，中英问法都回中文。
5. 译文按内容哈希缓存（含内存 + 本机文件 `~/.codex/translate-cache.json`），
   相同的话只花一次钱；多轮对话是**增量翻译**——历史里翻过的部分直接命中
   缓存，每轮只为新增的那几句付费。所以 token 花得很少：一轮普通中文问答
   通常就一两百 token，缓存命中的轮次零花费。
6. **尺寸上有两道保险**：一片文本既受字符数上限（1500）也受估算 token 上限
   （约 2000）约束，超长行在 rune 边界切开；翻译服务嫌「太大」时网关自己
   对半切开重试（最多 3 层），而不是把这一轮打死。同一段文本的多个片段并行
   翻译，等的是最慢的一片，不是所有片之和。
7. **上游装不下译文时，从最老的消息开始少翻**：Control Page 的「上游上下文
   上限」填上你模型的窗口（例如 `128000`，0 = 不检查）后，网关会估算「翻译
   完的请求」有多大；如果翻译才是压垮请求的原因，就把最老的历史留在中文
   原样发过去，**最新一轮永远翻译**，并在日志和「预算放弃」计数里写明。
   请求本身就已经超窗口时不做这种妥协（翻译不是原因）。

### 为什么回复是中文的

因为网关在每轮**首条用户文本之前**统一前置一句固定英文指令
`Please reply in Chinese.`（中英问法都一样）。上游看到的是“英文问题 +
英文指令”，于是用中文回答你——中文回复是你**要求**来的，不是翻译回来
的。你发英文也回中文，同理。

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

### ### Control Page 能看到什么

翻译请求数、缓存命中、已拒绝、增删改查的增量/全量、累计翻译字符、
Prompt/Completion/Total Tokens、备用翻译次数、预算放弃次数。
只记数不记正文不记 key。

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
./scripts/install-plugin.sh          # Linux / macOS
```

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install-plugin.ps1   # Windows
```

两个脚本做同一件事：`go build` 三个二进制 → 装到 `~/.local/bin`（Windows 是
`%USERPROFILE%\.codex\bin`）→ 拷插件 → 装自启（Linux `.desktop`、macOS LaunchAgent、
Windows 启动文件夹快捷方式）→ 合并个人 marketplace 条目。Windows 脚本还会把插件的
`hooks.json` 换成 PowerShell 版本（`hooks.windows.json`），因为 Codex 在 Windows 上
不一定有 `sh`。

## 构建与运行（Windows / macOS）

三个二进制都是纯 Go（唯一依赖 godbus 只在 Linux 托盘里用到），交叉编译即得：

```sh
# Windows（在 Linux/macOS 上交叉编译，或直接在 Windows 上 go build）
GOOS=windows GOARCH=amd64 go build -o codex-watchdog.exe ./cmd/codex-watchdog
GOOS=windows GOARCH=amd64 go build -o codex-fronthost.exe ./cmd/codex-fronthost
GOOS=windows GOARCH=amd64 go build -o codex-translate.exe ./cmd/codex-translate
# 想让它没有控制台黑窗，加 -ldflags "-H windowsgui"（日志仍可在 Control Page 看到）

# macOS
GOOS=darwin GOARCH=arm64 go build -o codex-watchdog ./cmd/codex-watchdog
GOOS=darwin GOARCH=arm64 go build -o codex-fronthost ./cmd/codex-fronthost
GOOS=darwin GOARCH=arm64 go build -o codex-translate ./cmd/codex-translate
```

跑 `codex-watchdog`（Windows 直接双击，或 `codex-watchdog.exe --no-tray` 只看网页），
然后打开 `http://127.0.0.1:18786/`。其余步骤与 Linux 相同，密钥、缓存、配置路径都
在 `~/.codex/`（Windows 是 `%USERPROFILE%\.codex\`）。

开机自启由安装脚本负责（不属于二进制本身）：Windows 脚本往启动文件夹放一个
`codex-lang.lnk`（目标 `codex-watchdog.exe`，窗口最小化；想完全无窗口就把二进制用
`-ldflags "-H windowsgui"` 再构建一次，README 上面的命令有注明）；macOS 脚本写
`~/Library/LaunchAgents/local.codex-lang.watchdog.plist`（`RunAtLoad`）并尝试
`launchctl bootstrap gui/$UID` 载入。想手动来也行：Windows 任务计划程序加一条「登录时运行」，
macOS 直接把可执行文件加进「登录项」。

跨平台上的已知差异：

- 托盘图标：Linux/Windows 有，macOS 没有（会打印一行提示，其余功能照常）；
- 前端点击、`/api/test`、翻译路由、`/api/logs` 等全部平台一致；
- Codex 插件的 `SessionStart` 钩子三平台齐了：`ensure_services.sh`（Linux/macOS，
  macOS 上没有 `setsid` 会自动退回 `nohup`）、`ensure_services.ps1` + `ensure_services.cmd`
  （Windows，脚本会切换 `hooks.json`）；
- 仍然是 Linux 专用的：AppImage 打包、`.desktop` 文件本身、以及 macOS 的菜单栏图标
  （要菜单栏图标就得引入 cgo + Objective-C 运行时，这个项目明确不做——那会毁掉交叉编译，
  而 Control Page 已经够用）。

## 目录结构

- `cmd/`：三个二进制入口；
- `internal/gateway/`：翻译、转发、缓存、计量；
- `internal/fronthost/`：前置轻进程；
- `internal/control/`：Control Page 与配置（`frontend/` 为页面）；
- `internal/opencodego/`：OpenCode 凭据约定；
- `internal/tray/`：托盘图标（Linux SNI / Windows 通知区 + `.ico` 渲染 / macOS 无图标桩）；
- `docs/spec/`：设计文档（最高策略见 `translation-policy.md`）；
- `docs/research/`：调研笔记；
- `pack/`：Codex 插件与打包文件；`scripts/`：安装脚本。

## 免责与说明

- 出生原因就是 GPT 繁忙难用，但**不承诺解决任何人的繁忙问题**，也不承诺
  任何效果；模型与上游策略随时会变，以你实测为准；
- Linux 已实测（Mint/Xfce 托盘）；Windows 的托盘与移植代码只做了编译与 `go vet`
  层面的验证，未在真实 Windows 桌面上跑过；macOS 能构建、能跑，但没有菜单栏图标；
- 你的中文原文会发给**你自己配置**的翻译服务（ unavoidable，做翻译就得给人看），
  介意者勿用；
- Issue 欢迎带 Control Page 日志行与指标截图（不要贴 key 和正文）。
