# Codex UserPromptSubmit: replace vs additionalContext

Question: on this machine’s current Codex, can a `UserPromptSubmit` hook (including a plugin-bundled hook) replace the User Prompt body, or can it only append `additionalContext` / intercept? How must a plugin hook be declared, and what trust step is required before it actually runs?

## Decision

**Cannot replace the User Prompt.** `UserPromptSubmit` can (1) append extra developer context via `hookSpecificOutput.additionalContext` or plain-text stdout, and/or (2) intercept by blocking the turn (`decision: "block"` + `reason`, `continue: false`, or exit code `2` with a stderr reason). There is no `updatedPrompt` / `updatedInput` field on this event. A plugin hook uses the same output schema; it is discovered only from an **enabled** plugin’s `hooks/hooks.json` (or a `hooks` entry in `.codex-plugin/plugin.json`), and Codex **skips** it until the current definition is reviewed and trusted (`[hooks.state]` `trusted_hash`).

## Local version clues (this machine)

Observed on 2026-09-08. No secrets copied from `~/.codex/config.toml` or auth files.

| Clue | Value | Source |
| --- | --- | --- |
| ChatGPT / Codex desktop app version | `26.901.51231` | `/usr/lib/chatgpt/resources/linux-package-metadata.json` (`version`); `/usr/lib/chatgpt/resources/owl-app.ini` (`AppVersion`) |
| Bundled Codex binary | `/usr/lib/chatgpt/resources/codex` (ELF, stripped) | filesystem |
| CLI reported by that binary and by `~/.local/bin/codex` | `codex-cli 0.153.4` | `codex --version` |
| Matching upstream tag | `rust-v0.153.4` → commit `3d2ee51ca2d5db578f328aa75e20aa22c0197c9a` | [github.com/openai/codex tag rust-v0.153.4](https://github.com/openai/codex/releases/tag/rust-v0.153.4) |
| Hooks feature in user config | `[features] hooks = true` | `~/.codex/config.toml` |
| Hook trust already in use | `[hooks.state]` entries with `trusted_hash = "sha256:…"` for `~/.codex/hooks.json` `SessionStart` handlers | `~/.codex/config.toml` |
| Strings in the bundled binary | `UserPromptSubmit`, `additionalContext` present; `updatedInput` present (PreToolUse); **no** `updatedPrompt` | `strings /usr/lib/chatgpt/resources/codex` |
| `plugin_hooks` feature on 0.153.4 | present as a **Removed** compatibility key and ignored (`continue` in the feature map) | [codex-rs/features/src/lib.rs @ rust-v0.153.4](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/features/src/lib.rs) |

`features.hooks` is Stable and **default-on** in 0.153.4; this machine also pins it true. `features.plugins` is Stable and default-on. The old `plugin_hooks` flag is not required.

## What UserPromptSubmit can return

Official Hooks docs for `UserPromptSubmit`:

- Input includes `prompt`: “User prompt that's about to be sent”. `matcher` is ignored.
- Plain text on stdout is “added as extra developer context”.
- JSON stdout may include `hookSpecificOutput.hookEventName = "UserPromptSubmit"` and `additionalContext`. That text is “added as extra developer context”.
- To block: `{ "decision": "block", "reason": "..." }` or exit code `2` with the reason on stderr.

Source: [developers.openai.com/codex/hooks](https://developers.openai.com/codex/hooks) (`### UserPromptSubmit`).

The generated wire schema for this event’s JSON output (`additionalProperties: false`) allows only:

- common fields: `continue`, `stopReason`, `systemMessage`, `suppressOutput`
- `decision` (`"block"` only), `reason`
- `hookSpecificOutput`: `{ hookEventName: "UserPromptSubmit", additionalContext?: string }`

No `updatedPrompt`, `updatedInput`, or other rewrite field.

Source: [user-prompt-submit.command.output.schema.json @ rust-v0.153.4](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/hooks/schema/generated/user-prompt-submit.command.output.schema.json)

Rust structs match that schema and use `#[serde(deny_unknown_fields)]`:

- `UserPromptSubmitCommandOutputWire`
- `UserPromptSubmitHookSpecificOutputWire` (`hook_event_name`, `additional_context` only)

Source: [codex-rs/hooks/src/schema.rs @ rust-v0.153.4](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/hooks/src/schema.rs)

The output parser copies `hook_specific_output.additional_context` and maps `decision: block` (with a non-empty `reason`) to `should_block`. It does not read a replacement prompt.

Source: [codex-rs/hooks/src/engine/output_parser.rs](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/hooks/src/engine/output_parser.rs) (`parse_user_prompt_submit`)

Event handler outcome is only `{ should_stop, stop_reason, additional_contexts }`. The inbound `prompt` is serialized to the hook as read-only input.

Source: [codex-rs/hooks/src/events/user_prompt_submit.rs @ rust-v0.153.4](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/hooks/src/events/user_prompt_submit.rs)

Contrast: **PreToolUse** can rewrite a tool call with `permissionDecision: "allow"` + `updatedInput`. That field exists on PreToolUse’s generated schema and is documented. It is not part of UserPromptSubmit.

Sources: [Hooks — PreToolUse](https://developers.openai.com/codex/hooks); [pre-tool-use.command.output.schema.json @ rust-v0.153.4](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/hooks/schema/generated/pre-tool-use.command.output.schema.json)

## What Codex does with the User Prompt after the hook

On a user turn, Codex runs `inspect_pending_input` → `run_user_prompt_submit`, then:

- If the hook **stops** (`should_stop`): it records any `additional_contexts` and **does not** call `record_pending_input` for that user item (the original User Prompt is not recorded as accepted input for that blocked path).
- If the hook **does not stop**: it records the **original** `TurnInput::UserInput` via `record_user_prompt_and_emit_turn_item`, then records hook context as separate conversation items.

Sources:

- [codex-rs/core/src/session/turn.rs @ rust-v0.153.4](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/core/src/session/turn.rs) (`run_hooks_and_record_inputs`)
- [codex-rs/core/src/hook_runtime.rs @ rust-v0.153.4](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/core/src/hook_runtime.rs) (`inspect_pending_input`, `record_pending_input`, `record_additional_contexts`)

`record_additional_contexts` maps each string to a developer-role conversation item (`HookAdditionalContext` → `ContextualUserFragment`). It never mutates the user message content.

So Injection for a translate plugin on this version is: keep the original User Prompt, then attach Translated Prompt + Reply Instruction as `additionalContext` (developer context after the user message). Blocking would drop the User Prompt from that turn rather than rewrite it.

`suppressOutput` is parsed on this event but currently discarded (`let _ = parsed.universal.suppress_output` in `user_prompt_submit.rs`). Official docs also say `suppressOutput` is “parsed today but not yet implemented” for the common output fields. Treat hook context as model-visible and typically UI-visible.

## How a plugin hook is declared

Official packaging:

1. Plugin root must have `.codex-plugin/plugin.json`.
2. Default lifecycle file: `hooks/hooks.json` inside the plugin root. If that path is used, a `hooks` field in the manifest is optional.
3. If `.codex-plugin/plugin.json` defines `hooks`, Codex uses **that** instead of the default file. The field may be a `./`-prefixed path, an array of such paths, an inline hooks object, or an array of inline objects. Paths resolve from the plugin root and must stay inside it.
4. Hook JSON uses the same event schema as user `hooks.json` (including `UserPromptSubmit` with `type: "command"`). Only `type: "command"` handlers run; `prompt` and `agent` handlers are parsed but skipped.
5. Plugin commands get `PLUGIN_ROOT` / `PLUGIN_DATA` (and Claude-compat `CLAUDE_PLUGIN_ROOT` / `CLAUDE_PLUGIN_DATA`).

Sources:

- [developers.openai.com/codex/hooks](https://developers.openai.com/codex/hooks) (`## Plugin-bundled hooks`)
- [developers.openai.com/codex/build-plugins](https://developers.openai.com/codex/build-plugins) (`### Bundled MCP servers and lifecycle hooks`)
- [developers.openai.com/codex/plugins](https://developers.openai.com/codex/plugins) (hooks must be reviewed/trusted)

Discovery sets `HookSource::Plugin` and `is_managed: false`.

Source: [codex-rs/hooks/src/engine/discovery.rs @ rust-v0.153.4](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/hooks/src/engine/discovery.rs) (`append_plugin_hook_sources`)

## What must be true before a plugin hook actually runs

Layered gates, all owned by Codex:

1. **Lifecycle hooks feature on.** `[features] hooks = true` (default). `hooks = false` turns hooks off. Alias `codex_hooks` is deprecated. Source: [Hooks — Turn hooks off](https://developers.openai.com/codex/hooks); [config-reference `features.hooks`](https://developers.openai.com/codex/config-reference); feature table in [codex-rs/features/src/lib.rs @ rust-v0.153.4](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/features/src/lib.rs) (`Feature::CodexHooks`, `default_enabled: true`).

2. **Plugin enabled.** Docs: “When a plugin is enabled, Codex can load lifecycle hooks from that plugin”. Desktop/CLI store on/off in `~/.codex/config.toml` as `[plugins."<id>"] enabled = true`. Source: [Build plugins — How the ChatGPT desktop app uses marketplaces](https://developers.openai.com/codex/build-plugins); [slash command `/plugins` Space to toggle](https://developers.openai.com/codex/cli/slash-commands).

3. **Not skipped by managed-only policy.** `allow_managed_hooks_only = true` skips user, project, session, **and plugin** hooks. Source: [Hooks — Managed hooks](https://developers.openai.com/codex/hooks).

4. **Trust of the exact current definition.** Plugin-bundled hooks are non-managed. “Installing or enabling a plugin doesn't automatically trust its hooks; Codex skips plugin-bundled hooks until you review and trust the current hook definition.” Trust is recorded against the hook’s current hash; changed hooks go back to review. UI: CLI `/hooks` (trust / disable). Automation: `--dangerously-bypass-hook-trust` for that invocation only.

Sources: [Hooks — Review and trust hooks](https://developers.openai.com/codex/hooks); [CLI `--dangerously-bypass-hook-trust`](https://developers.openai.com/codex/cli/reference)

Runtime gate in discovery: a handler is added to the runnable set only if `enabled` and (`bypass_hook_trust` **or** `trust_status` is `Managed` | `Trusted`). Plugin hooks are never managed. Status:

- matching `trusted_hash` → `Trusted`
- stored hash differs → `Modified`
- no stored hash → `Untrusted`

Source: [codex-rs/hooks/src/engine/discovery.rs @ rust-v0.153.4](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/hooks/src/engine/discovery.rs) (`hook_trust_status`, runnable `handlers.push` condition)

Persisted key format:

- `plugin_hook_key_source` = `{plugin_id}:{source_relative_path}`
- `hook_key` = `{key_source}:{event_label}:{group_index}:{handler_index}`
- `UserPromptSubmit` label = `user_prompt_submit`

Example: `my-plugin:hooks/hooks.json:user_prompt_submit:0:0` under `[hooks.state."<key>"] trusted_hash = "sha256:…"`.

Sources: [codex-rs/hooks/src/declarations.rs @ rust-v0.153.4](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/hooks/src/declarations.rs); [codex-rs/hooks/src/lib.rs @ rust-v0.153.4](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/hooks/src/lib.rs) (`hook_key`, `hook_event_key_label`)

This machine already persists that table for user-level `SessionStart` hooks, which is the same trust mechanism a plugin `UserPromptSubmit` would use.

## Implications for Injection

On Codex 0.153.4 / desktop 26.901.51231:

- A Codex Plugin **cannot** swap the User Prompt for a Translated Prompt in place.
- It **can** leave the original User Prompt in place and append Translated Prompt + Reply Instruction as `additionalContext`.
- It **can** block the turn, which withholds the User Prompt rather than replacing it.
- After install/enable, the author still has to trust the hook in `/hooks` (or bypass trust for a one-off CLI run). Changing the command string or hook JSON invalidates the hash and skips the hook until re-trusted.
