---
mkskill:
  pos: 40
---

## Flags

`info`:

| Flag | Default | Description |
|---|---|---|
| `-blob` | | Blob file to read (required) |
| `-short` | | One-line summary instead of the full header |

`list`:

| Flag | Default | Description |
|---|---|---|
| `-blob` | | Blob file to read (required) |
| `-md` | (default) | Emit the markdown table |
| `-json` | | Emit JSON `{id,…,items:[…]}` instead (mutually exclusive with `-md`) |
| `-o` | stdout | Write to this file instead of stdout |

`manifest`:

| Flag | Default | Description |
|---|---|---|
| `-dir` | | Directory to scan (required) |
| `-o` | | Write the JSON manifest to a file (else stdout) |
| `-base` | scanned dir | Write `src` relative to this root (portable) |
| `-include` | | Comma-separated name globs to include |
| `-exclude` | | Comma-separated name globs to exclude |
| `-recurse` | | Recurse into subdirectories |
| `-nocase` | | Case-insensitive globbing; marks the blob case-insensitive |

`create`:

| Flag | Default | Description |
|---|---|---|
| `-manifest` | | Manifest to build from (required) |
| `-out` | | Output `.blob` file (required) |
| `-dir` | | Fill each item's `src` as `<dir>/<url>` when the manifest has none |
| `-id` | manifest's id, else fresh | Guid to stamp |
| `-base` | manifest's dir | Resolve relative `src` paths against this dir |
| `-nocase` | | Force the blob case-insensitive |
| `-strict` | | Fail (not warn) if the rebuilt data CRC differs from the manifest's `dataCRC32` |
| `-skip-unchanged` | | No-op when the output already exists with the pinned id (no source file is read) |

`dump`:

| Flag | Default | Description |
|---|---|---|
| `-blob` | | Blob file to read (required) |
| `-baseout <dir>` | | Base directory the asset files are extracted into |
| `-manifest <file>` | | Full mode: also write the manifest to this file (`src` points at the extracted files) |
| `-file <key>` | | Single-entry mode: extract only the entry with this key |
| `-out <file>` | | Single-entry destination: an explicit path (a dir if it ends in a separator) |
| `-stdout` | | Single-entry destination: write the entry's bytes to stdout |

> **Full mode** (`-baseout` only) extracts every entry; `-manifest <file>` additionally
> emits the round-trippable manifest. **Single-entry mode** (`-file <key>`) extracts just
> that one entry — by key — to exactly one of `-baseout <dir>` (kept at its subpath),
> `-out <file>`, or `-stdout`. `-out`/`-stdout` apply only with `-file`.

`serve`:

| Flag | Default | Description |
|---|---|---|
| `-config` | | JSON server config: the config names the blobs to open |
| `-auto` | | Blob to serve from the config **it carries inside** |

> Exactly one of the two: `-config` and `-auto` cannot be combined, and nothing is merged.

`generate-claude-skill`:

| Flag | Default | Description |
|---|---|---|
| `-dst` | `.claude/skills/mskblob/SKILL.md` | Destination path (mutually exclusive with `-global`) |
| `-global` | | Install into `~/.claude/skills/mskblob/SKILL.md` (available from every project) |
| `-force` | | Overwrite an existing destination file |

`generate-agent-docs`:

| Flag | Default | Description |
|---|---|---|
| `-dst` | `AGENTS.md` | Destination path |
| `-force` | | Overwrite an existing destination file |

