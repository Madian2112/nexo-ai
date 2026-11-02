# nexo-ai

One source directory for skills, MCP servers, hooks, permissions, rules, the agent's soul and custom agents, compiled into the native configuration of **Claude Code**, **Kiro CLI v3**, **Codex** and **OpenCode**.

```bash
nexo-ai import all                # bring your current agent configs into the source, once
nexo-ai install claude            # or kiro, codex, opencode, all
nexo-ai install all --dry-run     # show what would change, write nothing
nexo-ai measure                   # estimated always-loaded tokens per agent
nexo-ai hook opencode --hooks F   # called by the generated OpenCode plugin, not by hand
```

> **Preview.** Expect rough edges. Back up your agent folders, or rely on `--dry-run` and the automatic backups, before the first install.

## Install

Download the binary for your OS (Windows, Linux or macOS, amd64 or arm64) from [Releases](https://github.com/Madian2112/nexo-ai/releases). Every file is signed with GitHub artifact attestations. Check that the file was built from this repository by its release workflow:

```bash
gh attestation verify nexo-ai_<version>_<os>_<arch>.tar.gz --repo Madian2112/nexo-ai
```

Or build it with Go:

```bash
go install github.com/Madian2112/nexo-ai/cmd/nexo-ai@latest
```

Then put your source in `~/.nexo-ai/source`, starting from `nexo-ai import all` or from [`source-example/`](source-example), and run `nexo-ai install all --dry-run`.

## Why

Every coding agent wants the same things configured in its own format. Copying them by hand drifts. nexo-ai keeps one canonical source and treats each agent as an adapter. It also aims to load as little as possible per session: the soul is split into sections, and each section is loaded only when it applies.

## Layers

| Layer | Status | What it does |
|---|---|---|
| A. Compiler | MVP 1, done | Source → native config per agent |
| C. Meter | MVP 2, done | Offline estimate of always-loaded vs on-demand tokens |
| Import | Done | Agent config → source, the source always wins |
| B. Runtime router | Planned | `UserPromptSubmit` hook that injects `intent` sections once per session |

## Source layout

Default location: `$NEXO_HOME/source` (`NEXO_HOME` defaults to `~/.nexo-ai`). See [`source-example/`](source-example).

```text
nexo.yaml               kiro.toolSearch (default true); models: alias → model per tool; hookAgent: {{agent}} per tool
soul/core.md            always loaded
soul/sections/*.md      frontmatter: inclusion always | fileMatch (globs) | intent (keywords)
skills/<name>/SKILL.md  Agent Skills standard (name, description)
mcp.yaml                servers: <name>: {command, args, env} or {url, headers}; {{home}} and {{env:NAME}} in values
hooks.yaml              hooks: [{name, event, matcher, command, timeout, agents}]; {{agent}} and {{home}} in command
permissions.yaml        rules: [{capability: read|write|shell|mcp, match: [...], effect: allow|ask|deny}]
agents/*.md             frontmatter name, description, model, mcp: [...]; body is the prompt
```

Hook events: `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Stop`. Matchers: `write`, `shell`, `read` or empty (any tool). `agents: [claude, kiro]` limits a hook to those agents. Without it, the hook goes to every agent. In `command`, `{{agent}}` becomes the agent name and `{{home}}` the home directory of the machine you install on, with forward slashes.

## Import

`nexo-ai import <agent|all> [--source DIR] [--dry-run]` reads an agent's current config and folds it into the source. The source is created if it does not exist. Run it before the first install, so the MCP servers, hooks and permissions you added by hand are not replaced.

- **The source always wins.** An entry that already exists (same name, or the same hook or permission) is kept. A differing imported one is reported, never written over.
- **What gets added:** new sections, skills, agents, MCP servers, hooks and permission patterns. Skills are copied into `skills/`, never linked to an agent directory.
- **Reported, not imported:** anything without a canonical equivalent. That covers hook events beyond the five canonical ones, matchers that mix tool kinds, tool-specific permissions such as `WebFetch(...)`, disabled MCP servers, agents without a description, and Claude agents' `tools` lists.
- **Kiro steering** with `manual` or `auto` inclusion is imported as `intent`, with the file name as its keyword. Review those keywords.
- **One hook set for every agent.** The same hook found in several agents becomes one hook that every agent gets, Codex included.
  - Home paths are compared as `{{home}}`, whichever way they are written: `$HOME`, `${HOME}`, `~/`, `%USERPROFILE%` or `C:/Users/<name>`.
  - The agent's own name becomes `{{agent}}`. So the Claude hook `"$HOME/agent-guards/bin/dispatch.js" --harness claude` and the Kiro hook `"C:/Users/<name>/agent-guards/bin/dispatch.js" --harness kiro` become one `"{{home}}/agent-guards/bin/dispatch.js" --harness {{agent}}`.
  - A Claude matcher that names write and shell tools together is split into one write hook and one shell hook, so it can match Kiro's.
- **A hook only one agent has stays in that agent** (`agents: [<agent>]`), because its command may only understand that agent's payload. Delete the `agents` line to share it with every agent.
- **Secrets.** MCP `env` and `headers` are copied as they are, often with tokens in them. `mcp.yaml` is created `0600`. Keep the source private.
- **Unsafe skills are refused.** A skill directory is skipped with a warning when it contains the source or your home, contains a symlink, is a repository checkout (`.git`), or holds a `.env` file.
- **One effect per pattern.** An imported permission that contradicts the source (an `allow` where the source says `deny`) is reported, not added.
- **Kiro prompts outside `~/.kiro` are not read.** An agent whose `prompt: file://...` points outside `~/.kiro` is skipped.
- **Comments are lost.** When `mcp.yaml`, `hooks.yaml` or `permissions.yaml` gain entries, they are rewritten without their comments. The original stays in the backup.
- **Verified after writing.** After writing, import loads the source again and fails if it does not load.

## Models

Each tool names models differently: Claude takes `opus` or `claude-opus-5-5`, Kiro takes `claude-opus-5.5`, Codex takes a GPT model, and OpenCode takes `provider/model`. So agents write an alias, and `nexo.yaml` maps it per tool:

```yaml
models:
  opus:
    claude: opus
    kiro: claude-opus-5.5
    opencode: anthropic/claude-opus-5-5
```

`model: opus` in an agent then writes each tool's own ID.
- **Missing tool.** A tool missing from the alias gets no `model` and uses its default model, with a warning.
- **Not an alias.** A model that is not an alias is written as is, as before.
- **OpenCode** only accepts `provider/model`.

## Placeholders

| Placeholder | Where | Becomes |
|---|---|---|
| `{{agent}}` | hook commands | `claude`, `kiro`, `codex` or `opencode`, or what `hookAgent` in `nexo.yaml` sets for that tool. OpenCode sends Claude-shaped payloads, so `hookAgent: {opencode: claude}` lets a dispatcher reuse its Claude support |
| `{{home}}` | hook commands, MCP command, args, env, url, headers | the home directory of the machine you install on, with forward slashes |
| `{{env:NAME}}` | MCP command, args, env, url, headers | an environment reference, read when the agent starts: `${NAME}` in Claude and Kiro, `{env:NAME}` in OpenCode |

Use `{{env:NAME}}` for anything that must not live in the source, such as tokens, internal hosts or project names. Codex does not expand variables in its config. So an env entry written exactly `NAME: {{env:NAME}}` goes into its `env_vars` whitelist instead, and any other env or header reference is reported and left out. A server with a reference in its command, args or url is left out of Codex entirely, because it would start with the literal text. `import` turns `${NAME}`, `{env:NAME}` and Codex `env_vars` back into `{{env:NAME}}`.

## What each agent gets

| Source | Claude Code | Kiro CLI v3 | Codex |
|---|---|---|---|
| core + `always` sections | `~/.claude/CLAUDE.md` | `~/.kiro/steering/nexo-*.md` (`inclusion: always`) | `~/.codex/AGENTS.md` |
| `fileMatch` sections | `~/.claude/rules/nexo-*.md` (`paths:`; loaded when a matching file is read or edited) | steering `inclusion: fileMatch` | skipped (no path-scoped instructions) |
| `intent` sections | not yet (router) | not yet | not yet |
| skills | symlink `~/.claude/skills/<name>` | symlink `~/.kiro/skills/<name>` | symlink `~/.agents/skills/<name>` |
| MCP | `mcpServers` in `~/.claude.json` | `mcpServers` in `~/.kiro/settings/mcp.json`, and `toolSearch.enabled` in `cli.json` | `mcp_servers` in `~/.codex/config.toml` |
| hooks | `hooks` in `~/.claude/settings.json` | `~/.kiro/hooks/nexo.json` | `~/.codex/hooks.json` (trust them with `/hooks`) |
| permissions | `permissions.allow/ask/deny` in `settings.json` | `~/.kiro/settings/permissions.yaml` | skipped (use `sandbox_mode` / `approval_policy`) |
| agents | `~/.claude/agents/<name>.md` | `~/.kiro/agents/<name>.json` + `prompts/<name>.md` | `~/.codex/agents/<name>.toml` |

**OpenCode** (`$XDG_CONFIG_HOME/opencode` when that variable is set, else `~/.config/opencode`, on Windows too):
- **Soul:** core + `always` sections go to `AGENTS.md`. `fileMatch` sections are skipped, because OpenCode has no path-scoped instructions.
- **Skills** are linked into `skills/<name>`. OpenCode ignores names that are not lowercase letters, digits and single hyphens.
- **Agents** go to `agents/<name>.md`. `model` is written only in `provider/model` form.
- **MCP servers** go to `mcp` in `opencode.json`: `type: local` with a command array, or `type: remote`.
- **Permissions** go to `permission.read/edit/bash` in `opencode.json`. They are written allow, then ask, then deny, because OpenCode applies the last matching rule.
- **Hooks.** OpenCode has no command hooks, so nexo-ai writes two files.
  - `nexo/hooks.json` holds the resolved hooks. It lives outside `plugins/`, because OpenCode loads every file in that folder.
  - `plugins/nexo-hooks.js` is an async shim of about 50 lines. It hands each event to the nexo-ai binary that installed it (`nexo-ai hook opencode --hooks nexo/hooks.json`).
  - The binary maps `tool.execute.before/after` → `PreToolUse`/`PostToolUse`, `chat.message` → `UserPromptSubmit`, `session.created` → `SessionStart` and `session.idle` → `Stop`. It runs the matching hooks with a Claude-shaped JSON payload, with OpenCode tools named `Bash`, `Edit`, `Write`, `MultiEdit`, `Read` and so on.
  - It answers whether to block. A `PreToolUse` or `UserPromptSubmit` hook blocks by exiting 2 or by printing `permissionDecision: deny`.
  - It fails open: a missing binary, an unreadable hooks file, a crash or a timeout let the event through. A timeout kills the hook and its children.
  - Hook output meant as extra context, such as `additionalContext` from `SessionStart`, is not passed to OpenCode.
  - The shim stores the binary's absolute path, so run `nexo-ai install opencode` again after moving nexo-ai.
  - `chat.message` is documented by third parties only.
- **MCP tool schemas** load at start, because OpenCode has no tool search.

Rules are copied, not symlinked: Claude Code only loads symlinked rules that point outside the project after an approval, and then never the path-scoped ones.

## Safety

- **Plan first.** Every agent is planned before anything is written. An unreadable `settings.json`, `.claude.json`, `mcp.json` or `config.toml` aborts the whole install.
- **Backups.** A file or directory is moved to `$NEXO_HOME/backups/<timestamp>/` before it is overwritten or removed. The only exception is an entry nexo-ai created that still matches the fingerprint recorded in `$NEXO_HOME/state/<agent>.json`. So a generated file you edited by hand, or a skill link you replaced, is backed up too.
- **Shared files.** In shared files, nexo-ai replaces only the keys it owns: `hooks`, `permissions.allow/ask/deny`, `mcpServers`, `mcp_servers`, `toolSearch.enabled`, and in OpenCode `mcp` and `permission.read/edit/bash`. Every other key is kept.
  - The keys nexo-ai writes are recorded in state.
  - When the source stops defining a key, the next install removes it.
  - A value you set in a key nexo-ai never wrote is left alone until the source defines that key.
- **File modes.** File modes are kept, so a `0600` `~/.claude.json` stays `0600`. Shared config files that nexo-ai creates are `0600`, because they can hold tokens.
- **TOML comments.** `config.toml` is rewritten without its comments. The original stays in the backup.
- **Stale entries.** Entries that nexo-ai created and that left the source are removed on the next install.
- **Names.** A skill, agent or section name must be a single file name, unique per kind. The name `core` is reserved for `soul/core.md`.
- **Idempotent.** A second install with the same source reports everything as `unchanged`, `--copy-skills` included.
- **Close the agent first.** Close the agent before installing: Claude Code rewrites `~/.claude.json` while it runs.

## Token estimates

`measure` divides characters by 3.8, because Anthropic does not publish its tokenizer. Use it to compare setups, not to predict a bill. It does not count the agent's own system prompt and built-in tools. MCP tool schemas are deferred by native tool search (Claude, Codex, and Kiro with `toolSearch.enabled`), so they are reported as notes.

## Build

```bash
go test ./...
go build -o bin/nexo-ai ./cmd/nexo-ai
GOOS=windows GOARCH=amd64 go build -o bin/nexo-ai.exe ./cmd/nexo-ai
```

On Windows without Developer Mode, symlinks fail and skills are copied instead. `--copy-skills` forces copying.

## Known limitations (preview)

- **Checked against docs, not yet on real installs:**
  - Kiro stores `toolSearch.enabled` as a flat key in `cli.json`.
  - Kiro custom agents pick up global steering.
  - OpenCode's `chat.message` plugin hook (documented by third parties only).
- **Not passed to OpenCode:** hook output meant as extra context.
- **No per-path permissions in Codex.** Codex gets no per-path permission rules and no `read` matcher. Reads go through its shell hook.
- **One command line for every OS.** A hook or MCP command written for one OS, such as a Windows `.exe` path, runs as written everywhere. There are no per-OS variants yet.
- **Not on the roadmap yet:** intent routing (`inclusion: intent`) and the per-prompt router.

## License

[MIT](LICENSE)
