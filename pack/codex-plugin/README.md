# codex-lang-ensure（个人 Codex 插件，只做拉起，不碰正文）

`SessionStart` 时确保两件事在跑：

1. 前置轻 Host（`codex-fronthost`，Codex 指向它）；
2. 真网关（`codex-watchdog --no-tray`，Control Page + 翻译网关）。

哪个没起就后台拉起谁。8 秒内起不来也不阻塞会话——本轮 Codex 对话会直接收到 HTTP 403（网关未就绪），不会连接被拒，更不会绕过翻译。

## 安装

```sh
./scripts/install-plugin.sh          # Linux / macOS
```

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install-plugin.ps1   # Windows
```

脚本会：`go build` 三个二进制 → 装到 `~/.local/bin`（Windows：`%USERPROFILE%\.codex\bin`）
→ 拷插件到 `~/.codex/plugins/codex-lang-ensure` → 写 `~/.agents/plugins/marketplace.json`
（个人 marketplace，不存在就建，已有就合并）→ 装自启。Windows 脚本还会把
`hooks/hooks.json` 换成 `hooks/hooks.windows.json` 的内容，因为 Codex 在 Windows 上
不一定有 `sh`。

然后：

1. 重启 ChatGPT/Codex 桌面（或只用 CLI 可跳过）；
2. `codex plugin add codex-lang-ensure@codex-lang-local`；
3. CLI 里开一轮跑 `/hooks`，把 `SessionStart` 那条 review 并 trust；
4. 验证：`curl -s http://127.0.0.1:18787/healthz` 应回 `{"ok":true}`；
5. 把 Control Page 的 TOML 贴进 `~/.codex/config.toml`。之后每开会话都会拉前置和 Watchdog；真网关听 `18788`，Codex 只打 `18787`。网关没起就是 403，不是连接被拒。

## hooks 目录

| 文件 | 用途 |
| --- | --- |
| `ensure_services.sh` | Linux/macOS 的正牌实现（POSIX sh；没有 `setsid` 时退回 `nohup`，没有 `python3` 时用 `sed` 取端口） |
| `ensure_services.ps1` | Windows 的正牌实现（PowerShell 5.1+，`Start-Process -WindowStyle Hidden` 拉起后台进程） |
| `ensure_services.cmd` | Windows 备用入口：Codex 用 cmd.exe 起 hook 时，转调同目录的 `.ps1` |
| `hooks.json` | 默认（POSIX）的 hook 声明 |
| `hooks.windows.json` | Windows 的 hook 声明，由 `install-plugin.ps1` 覆盖到 `hooks.json` |

改了其中任何一个都要在本机 `/hooks` 里重新 review + trust，否则 Codex 会跳过它。
两套实现的行为要一起改（同一个端口探测 → 拉起 → 重新读端口 → 打开前置的流程）。

## 行为边界

- hook 只做端口探测 + 拉进程，不读提问正文、不读密钥、不记 body；
- 成功静默（不污染会话上下文），失败只写 `~/.codex/logs/ensure.log`；
- 并发开多个会话同时触发也安全：抢绑输的一方直接退出，赢的一方服务大家；
- 改了 `hooks/` 下任何文件后要去 `/hooks` 重新 trust，否则 Codex 会跳过它；
- `allow_managed_hooks_only = true` 会跳过本插件（预期行为，不是 bug）。
