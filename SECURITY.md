# Security policy

## Supported versions

nexo-ai is in preview. Only the latest release gets security fixes.

## Reporting a vulnerability

Do not open a public issue. Report it privately through
[GitHub private vulnerability reporting](https://github.com/Madian2112/nexo-ai/security/advisories/new).
Include the version (`nexo-ai version`), your OS, and the steps to reproduce.

## What nexo-ai does with your system

These are by design. Report them only if nexo-ai does more than described here:

- `install` writes and merges config files in your agents' folders (`~/.claude`, `~/.kiro`, `~/.codex`, `~/.config/opencode`, `~/.agents`). It backs up whatever it did not create before overwriting it.
- `hook opencode` runs the hook commands from your own `hooks.yaml`, with the permissions of your user.
- MCP values written as `{{env:NAME}}` are read from the environment by each agent and never stored by nexo-ai. Any other value in your source, tokens included, is copied into your agents' config files as written.
