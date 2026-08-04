---
mkskill:
  pos: 30
---

## Commands

| Command | Description |
|---|---|
| `info` | Print the header (id, entries, dataCRC, size, nocase) |
| `list` | List the header + entries as a markdown table or JSON |
| `manifest` | Scan a directory into a reviewable JSON manifest |
| `create` | Build a blob from a manifest |
| `dump` | Extract a blob's asset files (all, or one by key with `-file`) |
| `serve` | Serve one or more blobs over HTTP from a JSON config |
| `generate-claude-skill` | Generate the Claude Code `SKILL.md` |
| `generate-agent-docs` | Generate the agent-agnostic `AGENTS.md` (Cursor, Aider, …) |

**Every option is a named flag**, so argument order never matters — a forgotten flag can just be added at the end. There are no positional arguments; an unknown flag or stray argument is reported with a clear error, and a command run with no flags prints its own help.

> On Windows, a trailing backslash inside quotes (`-dir "C:\path\"`) escapes the closing quote and merges it with the next argument — drop the trailing backslash.

