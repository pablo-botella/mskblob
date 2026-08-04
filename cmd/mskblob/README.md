# mskblob (CLI)

[![Go Reference](https://pkg.go.dev/badge/github.com/pablo-botella/mskblob/cmd/mskblob.svg)](https://pkg.go.dev/github.com/pablo-botella/mskblob/cmd/mskblob)

Command-line interface for [mskblob](https://pkg.go.dev/github.com/pablo-botella/mskblob) — read, create, inspect, dump and serve `.blob` pack files.

A `.blob` bundles many assets (their bytes plus a self-describing index) into one sidecar kept **outside** the Go binary, so the binary stays small and the assets are redeployed without recompiling. This CLI is the build-time and inspection side of the format; the [`mskblob`](https://pkg.go.dev/github.com/pablo-botella/mskblob) Go package is the runtime side.

## Install

```
go install github.com/pablo-botella/mskblob/cmd/mskblob@latest
```

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
| `-config` | | JSON server config (required) |

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

## Inspect a blob

A blob is fully self-describing, so these read only the file you point at — no source tree needed.

```
mskblob info -blob img.blob            # header: id, entries, dataCRC, size, nocase
mskblob info -blob img.blob -short     # one line
mskblob list -blob img.blob            # header + items table (markdown) to stdout
mskblob list -blob img.blob -json      # JSON to stdout (redirect with > if you want)
mskblob list -blob img.blob -json -o out.json   # JSON written to a file
```

## Build a blob

`create` reads a manifest and packs the bytes. The manifest may be a JSON object `{id?, nocase?, items:[{url, src, [key], [filename], [restype]}]}`, a bare `[ … ]` items array, or a text list of `"<url>\t<src>"` lines.

```
mskblob manifest -dir ./assets -include "*.png,*.jpg" -recurse -base ./assets -o img.json
mskblob create   -manifest img.json -out img.blob -base ./assets
```

Relative `src` resolves against `-base` (default: the manifest's own dir). The guid is taken from `-id`, else the manifest's `id`, else freshly generated.

### Skipping an unchanged rebuild

A pinned id *is* the content's identity, so if the output blob already carries that id the bytes are the same by contract. `-skip-unchanged` makes `create` a **no-op** in that case: it reads only the 128-byte header (no source file is touched) and leaves the file as is.

```
mskblob create -manifest img.json -out img.blob -id 947d88a9-… -skip-unchanged
```

It needs a pinned id; with an auto id every build mints a fresh guid, so there is nothing to compare and the build runs (with a warning). Handy in projects that rebuild many blobs repeatedly. To force a rebuild, delete the `.blob` or change the id.

## Edit a blob (dump → edit → create)

A blob is **not updatable in place** — rewriting one entry would shift every following offset and the data CRC. The edit loop is explicit:

```
mskblob dump   -blob img.blob -baseout ./out -manifest ./out/manifest.json
# …edit files and/or ./out/manifest.json…
mskblob create -manifest ./out/manifest.json -out img2.blob
```

`dump` extracts the asset files into `-baseout`; pass `-manifest <file>` to also get the manifest, whose `src` points at the just-written files, so `create -manifest <file>` re-creates the blob (no `-dir` needed). Entry paths are validated — an entry that would escape the base dir (`..`, absolute) is rejected.

### Extract a single entry

`dump -file <key>` pulls just the entry with that **key** to one destination:

```
mskblob dump -blob img.blob -file /patata/frita.png -baseout ./out  # ./out/patata/frita.png
mskblob dump -blob img.blob -file /patata/frita.png -out frita.png  # ./frita.png (cwd-relative)
mskblob dump -blob img.blob -file /patata/frita.png -out ./imgs/    # ./imgs/frita.png (dir → basename)
mskblob dump -blob img.blob -file /patata/frita.png -stdout         # raw bytes to stdout
```

`-baseout` keeps the entry at its subpath; `-out` is an explicit path (a directory if it ends in a separator, then the basename is appended); `-stdout` writes the raw bytes. `-out`/`-stdout` apply only with `-file`, and a missing or duplicate destination is an error.

## Serve blobs over HTTP

```
mskblob serve -config server.json
```

```json
{
  "addr": "0.0.0.0:8080",
  "tls": { "cert": "cert.pem", "key": "key.pem" },
  "vars": { "title": "My site" },
  "headers": [ { "name": "Cache-Control", "value": "public, max-age=3600" } ],
  "blobs": [
    { "file": "img.blob",  "base": "/img/", "id": "947d…" },
    { "file": "site.blob", "base": "/", "vars": { "section": "home" } }
  ]
}
```

Each blob mounts as a sub-mux under its `base`; static entries stream lazily and **template** entries are rendered (`html/template`) with the merged variables. `vars` and `headers` exist at two levels — global and per-blob — and merge, the blob's winning. Serves HTTPS when `tls.cert`/`tls.key` are set, else plain HTTP. All fields except `blobs[].file`/`base` are optional (`addr` defaults `:8080`, `base` defaults `/`).

## Generate the AI docs

`SKILL.md` (Claude Code) and `AGENTS.md` (tool-agnostic) are composed from the `ai/` sources — edit those, not the generated files, then re-run:

```
mskblob generate-agent-docs                  # → AGENTS.md
mskblob generate-claude-skill                # → .claude/skills/mskblob/SKILL.md (project-local)
mskblob generate-claude-skill -global        # → ~/.claude/skills/mskblob/SKILL.md (every project)
```

`-global` installs the skill under your own home, so it needs **no elevation/admin**; it is mutually exclusive with `-dst`. Use `-force` to overwrite an existing destination.

## See also

- [Library documentation](https://pkg.go.dev/github.com/pablo-botella/mskblob) — the `mskblob` package API (`Write`, `Open`, `Load`, `Blob.Handler`, …)
- [Project README](https://github.com/pablo-botella/mskblob#readme) — concepts, the manifest shape, the on-disk binary format, design notes
- [miniskin](https://pkg.go.dev/github.com/pablo-botella/miniskin) — the build-time assembler that produces these `.blob` files

## License

MIT
