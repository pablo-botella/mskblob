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
| `serve` | Serve one or more blobs over HTTP from a JSON config — a file, or the one a blob carries inside |
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

Relative `src` resolves against `-base` (default: the manifest's own dir). The guid is taken from `-id`, else the manifest's `id`, else freshly generated. A source that is the output file itself is rejected — a blob cannot include itself.

### A blob inside a blob

An entry can hold another blob, so a whole tree ships as one file. Build bottom up, one `create` per level, and declare the inner one with `restype` **`mskblob,nomux`**, a `key` and no `url`:

```
mskblob create -manifest docs.json -out docs.blob   # the inner blob
mskblob create -manifest site.json -out site.blob   # the container
```

```json
[
  { "url": "index.html", "src": "index.html" },
  { "key": "/docs", "restype": "mskblob,nomux", "src": "docs.blob" }
]
```

`mskblob` and `auto` are mskblob's own type flags, and whatever carries one is never served: such an entry **must** have a key, no url and `nomux` as well, or `create` refuses to build the blob. A nested blob is not reachable through its parent — `list` shows it as one more entry and `dump` extracts it as a file; to serve it, name it in the server config (see `internal_path` below).

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
mskblob serve -config server.json   # the config names the blobs
mskblob serve -auto site.blob       # the blob carries its config
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

Each blob mounts as a sub-mux under its `base`; static entries stream lazily and **template** entries are rendered (`html/template`) with the merged variables. `vars` and `headers` exist at two levels — global and per-blob — and merge, the blob's winning. Serves HTTPS when `tls.cert`/`tls.key` are set, else plain HTTP. Only `blobs[].file` is required (`addr` defaults to `:8080`, `base` to `/`). An entry flagged `nomux` is never routed, whatever its url.

### Serving a blob nested in another

A nested blob is served only when the config names it — nothing is mounted by itself. `internal_path` lists the **keys to descend through**, outermost first, one nested blob per element; it is an array because a key may contain slashes of its own:

```json
{
  "blobs": [
    { "file": "site.blob", "base": "/" },
    { "file": "site.blob", "base": "/docs/",    "internal_path": ["/docs"] },
    { "file": "site.blob", "base": "/docs/es/", "internal_path": ["/docs", "/es"], "id": "947d…" }
  ]
}
```

The nested blob is read in place, straight from its section of the file, and a file listed by several entries is opened once. `id` checks the blob **finally mounted**. A key that does not exist, or is not a nested blob, stops the server from starting.

### Default document and extension-less URLs

A mount serves exact matches only: `/` and `/docs/` are a 404, because no entry has that url. Two options, **per mount** and off unless declared, say what else to try when the URL is not an entry — each a list tried in order, the first hit winning:

```json
{ "file": "site.blob", "base": "/",
  "remove_extensions": [".html"],
  "default_document": ["index.html"] }
```

| Option | Applies when the URL… | Tries | Example |
|---|---|---|---|
| `remove_extensions` | has no extension, or ends in `/` | the URL (minus its slash) + each extension | `/about` → `about.html` |
| `default_document` | is the root, or ends in `/` | the URL + each name | `/docs/` → `docs/index.html` |

An entry by the exact URL always wins and is served as it is — there are **no redirects**, so `/about.html` keeps working next to `/about`. When both apply the extensions go first. Nothing else is guessed: `/docs` without its slash is not the folder and stays a 404. A `nomux` entry is never found this way; a template found this way is rendered like any other.

### HTTPS with an encrypted key

`tls.cert` and `tls.key` are PEM files. The key may be **encrypted** — PKCS#8 with PBKDF2 and AES or triple-DES, what `openssl` writes (`BEGIN ENCRYPTED PRIVATE KEY`). Its password is never in the config, only where to find it:

```json
"tls": { "cert": "cert.pem", "key": "key.pem",
         "password_file": "/run/secrets/tls_password",
         "password_env": "MSKBLOB_TLS_PASSWORD" }
```

`password_file` is a file holding the password (a trailing newline is dropped) — a mounted Docker/Kubernetes secret fits; `password_env` names an environment variable. With both, the file wins, and the variable is used when the file is not there. An unencrypted key needs neither. A key derived with scrypt, a legacy `DEK-Info` PEM or a PFX is refused with a message saying so.

### Self-contained: the blob carries its own config

With `-config` the config is the entry point and names the blobs to open. `-auto` turns it round: the blob is the entry point and brings the config that serves it — one file to deploy.

```
mskblob serve -auto site.blob
```

The config is an entry of the blob at the fixed key **`/mskblob/auto/site.json`**, flagged `auto,nomux` and with no url, so it is never served itself:

```json
{ "key": "/mskblob/auto/site.json", "restype": "auto,nomux", "src": "site.json" }
```

Its content is the same JSON as above, except that `file` may be left out, meaning **this same blob** — the config cannot know what the file will be called once deployed. `internal_path` then descends from it, and a `file` with a value is still an external file:

```json
{
  "blobs": [
    { "base": "/", "default_document": ["index.html"] },
    { "base": "/docs/", "internal_path": ["/docs"] }
  ]
}
```

If the entry is missing, lacks `auto` or `nomux`, has a url, or is not valid JSON, the server does not start. The config of a nested blob is ignored when its parent is served. `-auto` and `-config` cannot be combined: to serve the blob another way, pass a config file and the one inside is not looked at.

The certificate can travel in the blob too. With `-auto`, `tls.cert` and `tls.key` are not paths but **keys of entries of the blob itself**, which must live under `/mskblob/` and be flagged `auto,nomux` — without those attributes they cannot be used:

```json
{ "key": "/mskblob/auto/cert.pem", "restype": "auto,nomux", "src": "cert.pem" },
{ "key": "/mskblob/auto/key.pem",  "restype": "auto,nomux", "src": "key.pem" }
```

```json
"tls": { "cert": "/mskblob/auto/cert.pem", "key": "/mskblob/auto/key.pem",
         "password_env": "MSKBLOB_TLS_PASSWORD" }
```

They are never served, but the blob now **contains the private key**, and `dump` extracts it like any entry: encrypt the key, so the file alone is not enough, and keep its password outside. To use a certificate on disk instead (say, one renewed without rebuilding the blob), serve with `-config`.

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
