---
mkskill:
  pos: 220
  in: ai*
---

## CLI

```
mskblob info     -blob <file>                  [-short]
mskblob list     -blob <file>                  [-md | -json] [-o f]
mskblob manifest -dir <dir>                    [-o f] [-base d] [-include g] [-exclude g] [-recurse] [-nocase]
mskblob create   -manifest <f> -out <blob>     [-id guid] [-base d] [-nocase] [-skip-unchanged]
mskblob dump     -blob <file> -baseout <dir>   [-manifest f]
mskblob dump     -blob <file> -file <key>      (-baseout d | -out f | -stdout)
mskblob serve    -config <file> | -auto <blob>
```

`go install github.com/pablo-botella/mskblob/cmd/mskblob@latest`

> **Every option is a named flag** — order never matters, so a forgotten flag can
> just be added at the end (`mskblob manifest -dir ./d -include "*.png" -nocase`).
> There are no positional arguments: an unknown flag or a stray argument is reported
> with a clear error, and a command run with **no** flags prints its own help.

- `list` prints a markdown doc (header section + items table) by default, or JSON
  `{id,…,items:[…]}` with `-json` (`-md`/`-json` are mutually exclusive). Output goes
  to stdout — view it, or redirect with `>` — unless `-o <file>` writes it to a file
  (same meaning as `manifest -o`). `info -short` is a one-liner.
- `manifest` scans `-dir` into a JSON manifest (not recursive unless `-recurse`;
  `-include`/`-exclude` globs on the file name) so flags can be reviewed by hand
  before `create`. `-base <dir>` writes `src` relative to that root (portable);
  `create -base <dir>` resolves it. `-nocase` makes the glob matching
  case-insensitive **and** marks the resulting blob case-insensitive (recorded in
  its header; `create -nocase` forces it too; `manifest -nocase` also warns about
  case-folded duplicate URLs). `info`/`list` show the `nocase` flag.
- `create` reads a JSON manifest (`{id?, nocase?, items:[{url, src, [key],
  [filename], [restype]}]}`, a bare `[…]` array, or a `<url>\t<src>` text list);
  relative `src` resolves against `-base` (default: the manifest's dir). For a
  manifest with **no** src (e.g. from `list -json`), `-dir <srcdir>` fills each
  item's src as `<srcdir>/<url>` — so a blob's `list` inventory round-trips back
  into a blob once you point it at the source files. (`dump -manifest <file>` writes
  a src-bearing manifest, directly create-able without `-dir`.) When the `id`
  is **pinned**, create verifies the content: if the manifest carries a `dataCRC32`
  the rebuilt CRC must match it, and if it carries **no** dataCRC32 the content
  can't be verified at all — either case is a warning, or an error with `-strict`.
  With an auto id there's nothing to check. `-skip-unchanged` makes `create` a no-op
  when the output blob already exists with the **pinned** id: a pinned id is the
  content's identity, so the bytes are the same by contract — no source file is read
  (only the 128-byte header) and the file is left untouched. It needs a pinned id
  (`-id` or the manifest's); with an auto id every build mints a fresh guid, so there
  is nothing to compare and the build runs (with a warning). Handy in projects that
  rebuild many blobs repeatedly; to force a rebuild, delete the `.blob` or change the id.
  A **nested blob** needs no special flag: give the item `restype: "mskblob,nomux"`, a
  `key`, no `url`, and a built `.blob` as `src`. Levels are built bottom up, one `create`
  each.
- `dump` is for **extracting the asset files**: it writes every entry under `-baseout`
  (its directory), each at its url — or at its **key** when it has none, as a template
  or a nested blob does. It does **not** write a manifest by default; `-manifest <file>`
  is an extra that also emits the manifest in one go, with each `src` pointing at the
  just-extracted files so `create -manifest <file>` round-trips the blob, nested blobs
  included. An entry that would escape the base dir, or has no name at all, is rejected.
- `dump -file <key>` extracts **just one entry** (looked up by **key**) to exactly one
  destination: `-baseout <dir>` (lands at the entry's subpath, e.g. key
  `/patata/frita.png` → `<dir>/patata/frita.png`), `-out <file>` (an explicit path,
  relative to the cwd — or a directory if it ends in a separator, then the entry's
  basename is appended), or `-stdout`. `-out`/`-stdout` apply only with `-file`, and a
  missing/duplicate destination is an error.
- **Not updatable**: to change a blob, `dump` it (use `-manifest` to get the manifest
  too), edit, then `create`. The guid is automatic unless you pass `-id`.

### serve — a web server from a JSON config

```sh
mskblob serve -config server.json
```

```json
{
  "addr": "0.0.0.0:8080",
  "tls": { "cert": "cert.pem", "key": "key.pem" },
  "vars": { "title": "My site", "env": "prod" },
  "headers": [ { "name": "Cache-Control", "value": "public, max-age=3600" } ],
  "blobs": [
    { "file": "img.blob",  "base": "/img/",  "id": "947d…" },
    { "file": "site.blob", "base": "/", "vars": { "section": "home" },
      "headers": [ { "name": "X-Frame-Options", "value": "DENY" } ] }
  ]
}
```

Mounts each blob as a sub-mux under its `base`; statics stream lazily and
**template** entries are rendered (`html/template`) with the merged variables.
`vars` and `headers` exist at **two levels — global and per-blob** — and merge, the
blob's winning. Serves HTTPS when `tls.cert`/`tls.key` are set, else plain HTTP
(fine behind a Cloudflare-style tunnel that terminates TLS). Only `blobs[].file`
is required — and not even that with `-auto` (`addr` defaults `:8080`, `base` defaults `/`).

**Nested blobs are served only when named.** `serve` never descends by itself, and a
nested blob has no url, so its parent 404s for it. Add `"internal_path": ["/docs", "/es"]`
to an entry — an **array of keys** to descend through, outermost first, one nested
blob per element (an array, not a slash-separated string: keys may contain slashes).
Absent/empty = the file itself. The same `file` may appear in several entries (the
container at `/`, a child at `/docs/`…); it is opened once and each nested blob is
read in place. `id` then verifies the blob finally mounted. An unknown key, or one
that is not a `mskblob` entry, is a startup error naming `internal_path[n]`.

**`serve -auto <blob>` — the blob carries its own config.** `-config`: the config
loads the blobs. `-auto`: the blob loads the config, from its entry with the fixed key
**`/mskblob/auto/site.json`**, which must be flagged `auto,nomux` and have no url
(manifest: `{"key": "/mskblob/auto/site.json", "restype": "auto,nomux", "src": "site.json"}`).
Same JSON as `-config`, except `blobs[].file` may be omitted = **this same blob**
(`internal_path` descends from it; a `file` with a value is an external file). Entry
missing / wrongly flagged / with a url / invalid JSON → startup error. `-auto` and
`-config` are mutually exclusive: nothing is merged. A nested blob's own config is
ignored when its parent is served.

**A URL that is not an entry is a 404 unless the mount declares otherwise.** Two
per-mount options, off by default, each a list tried in order (first hit wins):
`"remove_extensions": [".html"]` — a URL with no extension or ending in `/` also
tries itself (minus the slash) + each extension (`/about` → `about.html`);
`"default_document": ["index.html"]` — the root or a URL ending in `/` also tries
itself + each name (`/docs/` → `docs/index.html`). Exact matches always win and are
served as they are: **no redirects**. Extensions are tried before documents. `/docs`
without its slash is **not** the folder (404): nothing undeclared exists. `nomux`
entries are never found this way; templates found this way are rendered.

**TLS.** `tls.cert`/`tls.key`: with `-config`, PEM **paths on disk**; with `-auto`,
**keys of entries of the blob itself**, which must be under `/mskblob/` and flagged
`auto,nomux` (else startup error) — e.g. `/mskblob/auto/cert.pem`, `/mskblob/auto/key.pem`.
The key may be an **encrypted PKCS#8** (PBES2: PBKDF2 + AES-CBC or 3DES-CBC, what openssl writes);
the password comes from `tls.password_file` (file content, trailing newline dropped)
or the env var named by `tls.password_env` — file wins, env is the fallback when the
file is absent; never from the config. Unencrypted keys need neither. scrypt, legacy
`DEK-Info` PEM and PFX are refused by name. A blob carrying its key contains a
secret (`dump` extracts it): encrypt the key and keep the password outside.

### Generating these docs

```
mskblob generate-claude-skill  [-dst .claude/skills/mskblob/SKILL.md] [-global] [-force]
mskblob generate-agent-docs    [-dst AGENTS.md] [-force]
```

Both are composed from the `ai/` sources (`generate-agent-docs` is tool-agnostic —
AGENTS.md, `.cursor/rules`, CONVENTIONS.md, …). `generate-claude-skill` writes the
project-local skill by default; `-global` installs it into the user-global skills
dir (`~/.claude/skills/mskblob/SKILL.md`) so the skill is available from every
project. The home dir is writable without elevation, so `-global` needs no admin
rights; it is mutually exclusive with `-dst`.
