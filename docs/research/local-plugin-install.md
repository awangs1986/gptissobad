# Local Codex plugin install and hook trust

Question: on this machine (Codex desktop + CLI, hooks already on), how should a personal Codex Plugin with a `UserPromptSubmit` command hook be packaged, hung on a local marketplace, installed, and hook-reviewed/trusted? What are the official paths vs what already exists under `~/.codex/config.toml`, `~/.agents`, and `~/.codex/plugins`?

This note does not implement a plugin and does not change `~/.codex/config.toml` or `~/.codex/hooks.json`.

## Gist

Use the official **personal marketplace**: package `.codex-plugin/plugin.json` plus `hooks/hooks.json` (`UserPromptSubmit` `type: "command"`), list it from `~/.agents/plugins/marketplace.json` with `source.path` `./.codex/plugins/<name>`, copy the plugin into `~/.codex/plugins/<name>`, install from the desktop Plugins Directory or `codex plugin add`, then trust the hook in CLI `/hooks`. This machine already has `[features] hooks = true` and trusted user `SessionStart` hooks; it has **no** personal marketplace yet.

## Recommended personal-install path (not implemented)

Later decision (ticket 09) can still choose repo vs home, but the path that matches a **machine-wide, unpublished, desktop+CLI** `UserPromptSubmit` hook is the official personal marketplace, not a repo marketplace and not a user-level `~/.codex/hooks.json` entry.

1. **Keep git canonical source in this repo.** Do not treat `~/.codex/plugins/cache/` as source.
2. **Package** a plugin folder with `.codex-plugin/plugin.json` (`name` kebab-case) and `hooks/hooks.json` containing a `UserPromptSubmit` command that uses `${PLUGIN_ROOT}` (see [Packaging](#official-packaging)). Do not add a root `plugin.json` (Agent Plugins manifest). Do not add a `UserPromptSubmit` handler to the existing `~/.codex/hooks.json`.
3. **Copy** that folder to `~/.codex/plugins/<plugin-name>/` (sibling of the existing `cache/` directory, not inside it).
4. **Create** `~/.agents/plugins/marketplace.json` (this path does not exist today). Marketplace root is `$HOME`, so `source.path` should be `./.codex/plugins/<plugin-name>`. Use a marketplace `name` that does not collide with `openai-bundled`, `openai-primary-runtime`, or `openai-api-curated`.
5. **Do not hand-edit** `~/.codex/config.toml`. Desktop auto-reads the personal marketplace file. CLI documents implicit default marketplaces; `codex plugin marketplace add ~` would write a `[marketplaces.*]` block and is unnecessary if discovery already sees `~/.agents/plugins/marketplace.json`.
6. **Install**: restart ChatGPT/Codex desktop, open Plugins, install from that local source; or `codex plugin add <plugin>@<marketplace>`. Enablement is stored as `[plugins."<plugin>@<marketplace>"] enabled = true`.
7. **Trust**: start a CLI session and run `/hooks`. Review the new plugin command, trust it. Codex records a hash under `[hooks.state]`. Re-trust after any command/definition change. Do not use `--dangerously-bypass-hook-trust` for normal use.
8. After editing the plugin: update the copy under `~/.codex/plugins/<plugin-name>/` and restart desktop so the `local` cache install refreshes.

Repo marketplace (`$REPO_ROOT/.agents/plugins/marketplace.json` + `$REPO_ROOT/plugins/`) is the official **in-repo** alternative. It is better while developing inside this git tree, but a `UserPromptSubmit` translator that should run in every desktop/CLI session is not scoped to this repo. Use personal marketplace for always-on; keep this repo as the git source and copy/sync into `~/.codex/plugins/<name>`.

## Official packaging

Owner: [Package your plugin](https://developers.openai.com/plugins/build/plugins) (same packaging rules on [Build plugins](https://developers.openai.com/codex/build-plugins)).

Every plugin has a required manifest at `.codex-plugin/plugin.json`. Only `plugin.json` belongs in `.codex-plugin/`. Optional siblings at the plugin root:

- `skills/`
- `hooks/` (lifecycle hooks; default file `hooks/hooks.json`)
- `.app.json` / `.mcp.json`
- `assets/`

Minimal identity:

```json
{
  "name": "my-first-plugin",
  "version": "1.0.0",
  "description": "Reusable greeting workflow"
}
```

Use a stable kebab-case `name`. Hosts use it as the plugin identifier.

A published-style manifest can also set `hooks` to a `./`-prefixed path (or an array of paths, or inline hook objects). If the file is the default `./hooks/hooks.json`, the `hooks` field is optional: Codex checks that default automatically. If `hooks` is set on the manifest, Codex uses that instead of the default file. Paths must start with `./`, resolve relative to the plugin root, and stay inside the plugin root.

Default plugin hook file shape (official example uses `SessionStart`; same wrapper for `UserPromptSubmit`):

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "python3 ${PLUGIN_ROOT}/hooks/session_start.py",
            "statusMessage": "Loading plugin context"
          }
        ]
      }
    ]
  }
}
```

Plugin hook commands get `PLUGIN_ROOT` (installed plugin root) and `PLUGIN_DATA` (writable data dir), plus compatibility `CLAUDE_PLUGIN_ROOT` / `CLAUDE_PLUGIN_DATA`.

This machine’s bundled plugins show two real shapes:

- **Inline manifest hooks** (not a `hooks/hooks.json` file): `browser` and `chrome` under `~/.codex/.tmp/bundled-marketplaces/openai-bundled/plugins/*/ .codex-plugin/plugin.json` set `"hooks": { "hooks": { "Stop": [ { "hooks": [ { "type": "mcp_tool", ... } ] } ] } }`. That is OpenAI’s bundled `mcp_tool` handler, not the personal `command` path.
- **Default-file command hooks**: Figma in the curated snapshot has `hooks.json` at the **plugin root** (`~/.codex/.tmp/plugins/plugins/figma/hooks.json`) with `PostToolUse` `type: "command"`, and **no** `hooks` field in `.codex-plugin/plugin.json`. Official default is still `hooks/hooks.json`, not a root `hooks.json`. Follow the documented default.

Do not ship a **root** `plugin.json` (Agent Plugins 1.0 layout) next to `.codex-plugin/plugin.json`. Official layout does not include that file. A community report on Codex 0.149 ([openai/codex#39895](https://github.com/openai/codex/issues/39895)) says a root `plugin.json` made the Agent Plugins loader win and **silently skipped** Codex `hooks` from `.codex-plugin/plugin.json`.

### `UserPromptSubmit` command hook

Owner: [Hooks](https://developers.openai.com/codex/hooks).

Event is supported during a turn. `matcher` is **ignored** for `UserPromptSubmit`. Only `type: "command"` handlers run; `prompt` and `agent` are parsed but skipped.

Suggested plugin-local file `hooks/hooks.json`:

```json
{
  "hooks": {
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "python3 ${PLUGIN_ROOT}/hooks/user_prompt_submit.py",
            "statusMessage": "Translating user prompt"
          }
        ]
      }
    ]
  }
}
```

stdin is one JSON object including `prompt` (the User Prompt about to be sent). JSON stdout may set `hookSpecificOutput.additionalContext` (extra developer context) or `decision: "block"`. Plain text stdout is also added as extra developer context. This note does not decide injection strategy (ticket 04 / 01).

Commands run with the session `cwd`. That is why plugin hooks should use `${PLUGIN_ROOT}` rather than a relative path from cwd.

Matching hooks from multiple files all run. A plugin `UserPromptSubmit` therefore coexists with the existing user `SessionStart` hooks in `~/.codex/hooks.json`; it must not replace them.

## Marketplace options

Owner: [Package your plugin](https://developers.openai.com/plugins/build/plugins) / [Build plugins](https://developers.openai.com/codex/build-plugins); CLI: [Command line options](https://developers.openai.com/codex/cli/reference) and `codex plugin marketplace --help` (CLI 0.153.4 on this machine).

A marketplace is a JSON catalog. Local/repo/personal marketplaces are **not** the public Plugins Directory.

### Files the desktop reads

The ChatGPT desktop app can read:

| Kind | Catalog file | Typical plugin source |
| --- | --- | --- |
| Repo | `$REPO_ROOT/.agents/plugins/marketplace.json` | `$REPO_ROOT/plugins/<name>` |
| Legacy repo | `$REPO_ROOT/.claude-plugin/marketplace.json` | (legacy-compatible) |
| Personal | `~/.agents/plugins/marketplace.json` | `~/.codex/plugins/<name>` |

`source.path` is relative to the **marketplace root**, not to `.agents/plugins/`. Keep it `./`-prefixed and inside that root. Marketplace root is the directory that contains `.agents/` (so for `~/.agents/plugins/marketplace.json` the root is `$HOME`, which is why personal `source.path` is commonly `./.codex/plugins/<name>`). Local `source` may also be a plain string path such as `"./plugins/my-plugin"`.

Each plugin entry must include `policy.installation`, `policy.authentication`, and `category`. Installation values include `AVAILABLE`, `INSTALLED_BY_DEFAULT`, `NOT_AVAILABLE`.

### CLI marketplace commands

Prefer `codex plugin marketplace add` over hand-editing `config.toml`:

```text
codex plugin marketplace add ./local-marketplace-root
codex plugin marketplace add owner/repo --ref main
codex plugin marketplace add https://github.com/example/plugins.git --sparse .agents/plugins
codex plugin marketplace list
codex plugin marketplace upgrade [name]
codex plugin marketplace remove <name>
```

Sources: local directory, GitHub shorthand, HTTPS/SSH Git. `--sparse` is Git-only.

`codex plugin marketplace list` prints marketplaces Codex is considering, “including implicitly discovered default marketplaces and configured marketplace snapshots.”

This machine’s `codex plugin marketplace list --json` (2026-09-08):

| name | root | in `config.toml`? |
| --- | --- | --- |
| `openai-primary-runtime` | `~/.cache/codex-runtimes/codex-primary-runtime/plugins/openai-primary-runtime` | yes, `[marketplaces.openai-primary-runtime]` `source_type = "local"` |
| `openai-bundled` | `~/.codex/.tmp/bundled-marketplaces/openai-bundled` | yes, `[marketplaces.openai-bundled]` |
| `openai-api-curated` | `~/.codex/.tmp/plugins` | **no** `marketplaceSource` in JSON (implicit/bundled) |

No personal (`~/.agents/plugins`) and no this-repo marketplace appear, because those files do not exist.

Local example on disk (official bundled shape): `~/.codex/.tmp/bundled-marketplaces/openai-bundled/.agents/plugins/marketplace.json` with `"name": "openai-bundled"` and `"path": "./plugins/codex-app-tools"` etc. That matches the documented repo layout (marketplace root contains `.agents/plugins/marketplace.json` and `plugins/`).

## Install

Owners: packaging docs (desktop local install); CLI help / [CLI reference](https://developers.openai.com/codex/cli/reference); slash commands: [Slash commands](https://developers.openai.com/codex/cli/slash-commands).

### Desktop (documented path for testing a local plugin)

1. Write the marketplace file.
2. Restart the ChatGPT desktop app.
3. Open Plugins (Codex, or ChatGPT with Work). Browse the local marketplace source and install.
4. After plugin file changes: update the directory the marketplace entry points to, restart desktop.

Official note: marketplace-add CLI is for authoring/catalog setup; “Use the ChatGPT desktop app to install and test a local plugin.”

Install cache (desktop docs):

`~/.codex/plugins/cache/$MARKETPLACE_NAME/$PLUGIN_NAME/$VERSION/`

For local plugins, `$VERSION` is `local`. The host loads that **cache copy**, not the marketplace path directly.

On/off state is stored in `~/.codex/config.toml`. Observed shape on this machine:

```toml
[plugins."browser@openai-bundled"]
enabled = true
```

### CLI

```text
codex plugin add PLUGIN@MARKETPLACE
codex plugin add PLUGIN --marketplace MARKETPLACE
codex plugin list
codex plugin list --available --json
codex plugin remove PLUGIN@MARKETPLACE
```

In a CLI session, `/plugins` browses installed and discoverable plugins; Space toggles enabled on an installed plugin.

Public/workspace publish is out of scope (map: do not publish).

## Hook review and trust

Owner: [Hooks](https://developers.openai.com/codex/hooks); feature flag: [Config basics](https://developers.openai.com/codex/config-basic); CLI: `/hooks` in [Slash commands](https://developers.openai.com/codex/cli/slash-commands).

Hooks are on by default. Canonical feature key is `[features] hooks = true` (`codex_hooks` is a deprecated alias). This machine already has `hooks = true`.

Discovery: `hooks.json` or inline `[hooks]` next to config layers (`~/.codex/`, project `.codex/`), **plus** hooks bundled with **enabled** plugins. Higher-precedence config does not replace other hook sources; they all load. Project-local hooks need a trusted project; user and plugin hooks still load from their own layers.

**Installing or enabling a plugin does not trust its hooks.** Plugin-bundled hooks are non-managed. Codex skips them until the user reviews and trusts the **current** definition. Trust is recorded against the hook’s hash; a changed command is skipped again until re-trusted.

Documented review UI:

1. If hooks need review at startup, Codex warns to open `/hooks`.
2. In the CLI composer, `/hooks` → pick the event → trust, disable, or re-enable non-managed hooks.

`--dangerously-bypass-hook-trust` runs enabled hooks without persisted trust for that invocation only (automation that already vets sources). Not the personal-install path.

Observed persistence on this machine (user hooks, not plugins): `~/.codex/config.toml` `[hooks.state."~/.codex/hooks.json:session_start:N:0"] trusted_hash = "sha256:..."`. Desktop and CLI share this file. Official docs describe `/hooks` on the CLI; they do not document a separate desktop-only trust screen. After a plugin install, trust the new `UserPromptSubmit` command via CLI `/hooks` so the hash is written the same way.

Do not put the translator in `~/.codex/hooks.json`. That file is already a user-layer `SessionStart` source (three command hooks). The plugin should bundle `UserPromptSubmit` so it can be installed/disabled as a plugin without editing that file.

## This machine (read-only)

Recorded 2026-09-08. CLI: `codex-cli 0.153.4` (`~/.codex/version.json` latest 0.153.4). Desktop app version seen in `config.toml` MCP env: `26.901.51231`. Hooks feature is on.

### `~/.codex/config.toml` (relevant sections only)

Not copied: `model_providers`, MCP env, auth.

- `[features] hooks = true`
- `[hooks.state]` — three trusted hashes for `~/.codex/hooks.json` `session_start` indices 0–2
- `[marketplaces.openai-bundled]` and `[marketplaces.openai-primary-runtime]` — local paths under `~/.codex/.tmp/bundled-marketplaces` and `~/.cache/codex-runtimes/...`
- `[plugins."<name>@<marketplace>"] enabled = true` for bundled/runtime plugins listed below
- No personal marketplace table, no third-party plugin enablement keys

### `~/.codex/hooks.json`

User-layer file. `SessionStart` command hooks: `gh-axi`, `chrome-devtools-axi`, `lavish-axi` (timeout 10). These are already trusted in `[hooks.state]`. Map: leave them in place. No `UserPromptSubmit` here.

### `~/.codex/plugins`

| Path | Role |
| --- | --- |
| `cache/<marketplace>/<plugin>/<version>/` | Installed copies of official plugins |
| `.plugin-appserver/`, `.remote-plugin-install-staging/` | runtime/staging dirs |
| **no** top-level personal plugin folder | personal source `~/.codex/plugins/<name>` does not exist yet |

Installed cache trees present: `openai-bundled` (`codex-app-tools`, `browser`, `unified-computer-use`, `visualize`), `openai-primary-runtime` (documents/pdf/spreadsheets/presentations/template-creator), `openai-curated-remote` (a few remote installs). No `.../local` version directory (no local-marketplace plugin installed).

### `~/.agents`

Exists. Contains `skills/` and `.skill-lock.json` only. **`~/.agents/plugins/` does not exist** — no personal `marketplace.json`.

### Bundled catalog shape (example to copy)

`~/.codex/.tmp/bundled-marketplaces/openai-bundled/.agents/plugins/marketplace.json`: top-level `name` + `interface.displayName`, `plugins[]` with `source.source = "local"`, `source.path = "./plugins/<name>"`, `policy.installation = "AVAILABLE"`, `policy.authentication = "ON_INSTALL"`, `category`.

## Caveats (follow the owner)

- **Do not hand-edit `config.toml` for marketplaces or plugin enablement.** Official CLI is `codex plugin marketplace add` / desktop or `codex plugin add`. This research did not run those (they would mutate config).
- **Repo marketplace ≠ always-on.** Desktop discovers `$REPO_ROOT/.agents/plugins/marketplace.json` for that repo. A global prompt hook should use the personal marketplace.
- **Cache vs source.** After install, Codex loads `~/.codex/plugins/cache/.../local` (or a version). Editing only the git tree or only `~/.codex/plugins/<name>` without refresh can leave the cache stale.
- **Historical plugin-hook bugs.** Official current docs say enabled plugins load bundled hooks. Older CLI (0.128) reports ([openai/codex#16430](https://github.com/openai/codex/issues/16430)) that plugin hooks did not spawn; later (0.149) they do unless a root `plugin.json` is present (#39895). This machine is 0.153.4. Still: no root `plugin.json`; verify with `/hooks` after install.
- **IDE extension.** Official Plugins overview states plugins are for ChatGPT Work / Codex desktop and Codex CLI, not Chat, the IDE extension, or mobile. This project’s desktop+CLI target matches that.

## Sources

- [Package your plugin](https://developers.openai.com/plugins/build/plugins) — packaging, marketplace JSON, personal vs repo paths, install cache, enablement in `config.toml`, plugin hook file and `PLUGIN_ROOT`
- [Build plugins](https://developers.openai.com/codex/build-plugins) — same packaging rules; personal copy-to-`~/.codex/plugins` steps
- [Hooks](https://developers.openai.com/codex/hooks) — discovery, trust/`/hooks`, plugin-bundled hooks, `UserPromptSubmit` IO, `[features] hooks`
- [Config basics](https://developers.openai.com/codex/config-basic) — `[features] hooks` default true, stable
- [CLI reference](https://developers.openai.com/codex/cli/reference) — `codex plugin` / `marketplace`, `--dangerously-bypass-hook-trust`, implicit default marketplaces
- [Slash commands](https://developers.openai.com/codex/cli/slash-commands) — `/plugins`, `/hooks`
- This machine: `~/.codex/config.toml` (sections listed above), `~/.codex/hooks.json`, `~/.codex/plugins`, `~/.agents`, `~/.codex/.tmp/bundled-marketplaces/openai-bundled`, `codex plugin marketplace list --json`, `codex plugin list`, `codex --version`
- Community only (not owner): [openai/codex#39895](https://github.com/openai/codex/issues/39895), [openai/codex#16430](https://github.com/openai/codex/issues/16430)
