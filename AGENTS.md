# mskblob

## Overview

`mskblob` bundles many resources — their bytes plus a full index — into one
self-describing `.blob` sidecar kept **outside** the Go binary.

It was **born to give support to alternate storage in miniskin**
([`github.com/pablo-botella/miniskin`](https://pkg.go.dev/github.com/pablo-botella/miniskin)) — the
build-time assembler that produces these `.blob` files, and supports blobs using
this component from **v0.3.12** — but it's generic enough to be **just another
blob**: use it for any heavy asset set (a large media directory, hundreds of
images) too big to `go:embed`. The binary stays small and the blob is regenerated
and redeployed without recompiling.

Module: `github.com/pablo-botella/mskblob` — standard library only, no third-party deps.
mskblob also works standalone.

```sh
go get github.com/pablo-botella/mskblob                           # library
go install github.com/pablo-botella/mskblob/cmd/mskblob@latest    # CLI
```

### When to use

- Serving a large set of static assets without bloating the binary.
- Reading / writing / inspecting `.blob` files.
- Wiring the package into an app (load + access) or a build step (pack).
- Shipping several independently built blobs as one file: a blob can hold another,
  mounted in place with `OpenBlob` (see [Nested blobs](#nested-blobs-a-blob-inside-a-blob)).
- Reading a blob from C or Harbour: `capi/` (C reader, `mskblob-c` tool, Harbour
  wrapper). Readers only, on purpose: blobs are built with Go, which runs anywhere;
  other languages just need to open them. See `capi/README.md`.

The package **lists and gives content** — it finds resources for you and streams
their bytes; it does not own a web server. The internal layout (binary format,
indexes, offsets, alignment) is private: the public API is stable across format
changes.

## Package API

### Build a blob (one shot)

```go
m := mskblob.NewManifest("")              // "" → fresh GUID; pass an id to pin it
m.AddFile("logo.png", "assets/logo.png")  // a static resource
m.AddItem(mskblob.Item{URL: "hero.jpg", Filename: "hero.jpg", RestType: mskblob.Static, Src: "assets/hero.jpg"})
n, _ := m.AddFolder("assets/prod-img", mskblob.FolderOptions{Recurse: true, Include: "*.jpg,*.png"})
id, err := m.Write("dist/img.blob")       // packs everything, returns the id
```

- `Item.URL` — lookup key, **relative** to the mount base (how it's served).
- `Item.Key` — a logical id (how non-served resources like templates are reached).
- `Item.Filename` — recorded source name; `Item.Src` — file to read at Write time.
- `Item.RestType` — type flags (`Static`, `HTMLTemplate`, `Parse`, `Response`, `Nomux`,
  `Mskblob`).
- `Item.Size/CRC32/Offset` are **computed** by `Write` (ignored on input).
- Items are written in `(URL, Key)` order, so the data and its CRC are reproducible;
  only the GUID varies unless pinned. The package-level `Write(path, items, opts)` is
  the same one shot without the builder.
- **Case-insensitive blobs:** set `Manifest.NoCase` / `Options.NoCase` (or
  `FolderOptions.NoCase` to also fold the scan globs). It records a header flag and
  makes runtime `GetByURL`/`GetByKey` fold case; `Header.NoCase` reports it. Default
  is case-sensitive.
- **Skip an unchanged rebuild:** set `Options.SkipUnchanged` with a **pinned**
  `Options.ID`. A pinned id is the content's identity, so if `path` already holds a
  blob with that id the bytes are the same by contract — `Write` returns that id
  without reading any `Src` or rewriting the file (only a cheap `ReadHeader`). With
  an empty id there's no stable identity to compare, so the write proceeds. Use
  `Load`/`ReadHeader` to *check* an id; `SkipUnchanged` *acts* on that check to spare
  the pack in build loops that touch the same blob repeatedly.

### Open

```go
b, err := mskblob.Open(path)              // open + read index
b, err := mskblob.Load(path, expectID)    // Open + verify GUID when expectID != ""
h, err := mskblob.ReadHeader(path)        // cheap: 128-byte header only (cache checks)
```

### Nested blobs (a blob inside a blob)

An entry whose bytes are themselves a blob: marked `Mskblob`, with a **key and no
url** (`Write` enforces both), so it never enters the routing index — it is mounted,
never served.

```go
child, err := b.OpenBlob("/img")             // mount it in place; an ordinary *Blob
child, err := b.LoadBlob("/img", expectID)   // same, with the GUID verified
```

The child is read over its own section of the parent — nothing extracted — and its
offsets stay relative to itself, so `OpenBlob` works on it again at any depth: one
addition per level, resolved when mounting, not on every read. Each level keeps its
own GUID, index and case rule.

The tree shares the descriptor the root opened: a child's `Close` is a no-op (it owns
nothing) and closing the root closes the subtree — later reads return `ErrClosed`
rather than hitting a freed descriptor. Composition is the usual build, bottom up:
write each inner blob as a file, then pack it as the `Src` of an `Mskblob` item.

### Listing & access (the package finds resources for you)

```go
for _, it := range b.Items() { ... }      // list everything
it := b.GetByURL("img/logo.png")          // *Item, nil if absent
it := b.GetByKey("/page")                 // *Item, nil if absent (how templates are reached)
data, err := b.Bytes(it)                  // read an entry whole
rdr := b.Reader(it)                       // *io.SectionReader — stream, nothing resident
```

`*Item` is a read-only handle (URL, Key, Filename, RestType, Size, …); offsets and
on-disk layout stay internal. Lookups are in-memory map reads — they can't fail, so
they return just `nil` when absent. An entry is reached **by URL and/or by Key**:
statics have a URL, templates usually only a Key.

### Serving (a building block, not a web server)

```go
const ( DispatchAuto = 0; DispatchDone = 1 ) // any other int = an HTTP status code
type Middleware func(w http.ResponseWriter, r *http.Request, it *Item) int

func (b *Blob) Handler(base string, mw Middleware) http.Handler
```

`Handler` is a **sub-mux**: register only the base on your own mux
(`yourMux.Handle(base, b.Handler(base, mw))`) and it routes the rest by relative
URL. The **default** (`mw == nil`) serves **only static** entries — streamed
lazily, `ETag` = crc32, `If-None-Match` → 304, `Content-Type` by extension; anything
else (template, response, or an absent URL) is treated as if it weren't there → 404.
An entry flagged `Nomux` is **never routed**: the handler takes it for absent — the
middleware gets `nil` too — so it is reachable only by key.

Pass a `Middleware` to do more. It is called for **every** request with the matched
item (`nil` when the URL is absent, so it can answer unknown routes), and its return
drives dispatch:

- `DispatchAuto` (0) — let the blob serve it (static only, as above);
- `DispatchDone` (1) — you already wrote the response (e.g. rendered a template);
- any other int — returned as that HTTP status via `http.Error`.

```go
mux.Handle("/site/", b.Handler("/site/", func(w http.ResponseWriter, r *http.Request, it *mskblob.Item) int {
    switch {
    case it == nil:                                return mskblob.DispatchAuto      // unknown → 404
    case it.RestType&mskblob.HTMLTemplate != 0:    render(w, r, it); return mskblob.DispatchDone
    default:                                        return mskblob.DispatchAuto      // static → streamed
    }
}))
```

`Handler` returns a plain `http.Handler`, so any router that can mount one works
(chi, gin, std). It is the **only** part touching `net/http`; the rest is
router-agnostic, so you can ignore `Handler` and serve from `GetByURL`/`Reader`
yourself. Type flags: `Static 0x01, HTMLTemplate 0x02, Parse 0x04, Response 0x08,
Nomux 0x10` (low byte: miniskin's) and `Mskblob 0x0100, MskBlobAuto 0x0200` (second byte: mskblob's own). `RestType.Names()` / `.String()` give the name / hex forms.

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

## Manifest format

One JSON shape is used everywhere — `create` reads it; `manifest`, `dump` and
`list` produce it:

```json
{
  "id": "947d88a9-7196-490c-87dd-9a8a1262b0ec",
  "count": 1,
  "nocase": false,
  "items": [
    {
      "url": "img/logo.png",
      "key": "/img/logo.png",
      "filename": "logo.png",
      "restype": "static",
      "src": "assets/logo.png",
      "crc32": "0x15BF411A",
      "sizeLow": "0x00000003",  "sizeHigh": "0x00000000",
      "offsetLow": "0x000000E0", "offsetHigh": "0x00000000"
    }
  ]
}
```

- **Declarative** (`url`, `src`, `key`, `filename`, `restype`) — what to pack. `src` is
  required to build, plus at least one of `url`/`key`; the rest optional (`filename`
  defaults to `url`, `restype` to `static`).
- **`restype`** is a human flag mask: comma-separated names
  `static,tpl,parse,rsp,nomux,mskblob,auto` (empty = none). On input a hex/decimal value
  (`"0x05"`, `5`) is also accepted. A nested blob is
  `{"key": "/img", "restype": "mskblob,nomux", "src": "dist/img.blob"}` — key, no url,
  `nomux`, and a `.blob` as source; each level has its own manifest, built bottom up.
  `mskblob` and `auto` are mskblob's own flags: an entry carrying one **must** have a
  key, no url and `nomux`, or the blob is not written.
- **Header** — `id`, `count` and `nocase` are always present (the shape is
  consistent even for a freshly-scanned manifest); `version`/`dataCRC32` appear only
  once a blob is built. `id` and `nocase` are read on build (id pins the guid,
  `nocase` marks the blob case-insensitive); `count` is recomputed.
- **Computed** (`crc32`, `sizeLow`/`sizeHigh`, `offsetLow`/`offsetHigh`, plus
  `version`, `count`, `dataCRC32`) — emitted for inspection and **ignored on build**
  (recomputed from the bytes). Fixed-width 32-bit hex strings; 64-bit size/offset
  are split low/high — readable and free of JSON float precision loss.

## Binary format (all integers little-endian)

```
BLOCK A — header (64 bytes)
  0  magic "MSPK" · 4 version u8 · 5 flags u8 (bit 0x01 = case-insensitive lookup) · 6 reserved[2]
  8  count u32 · 12 dataCRC u32 (crc32 of block D) · 16 dataOffset u32 · pad→64
BLOCK B — id (64 bytes): guid ASCII, zero-padded (the sync token)
BLOCK C — index (count entries, each padded to a 16-byte boundary)
  entrysize u32 · key\0 · size u64 · crc32 u32 · restype i32 · url\0 · filename\0 · pad→16
BLOCK D — data: concatenated bytes at dataOffset; each entry's offset is the
  running sum of the preceding sizes
```

Every entry carries its full identity, so the blob is self-describing: it can be
inspected and served on its own, and its header alone is enough to verify a
deployed file (via the GUID).

Nesting adds nothing to this: a container is a normal blob, and the child is the
same format read over its own section — its `dataOffset` and entry offsets are
relative to itself, so it is valid wherever it sits and can be copied in or out
byte for byte. The only absolute number is the one the reader adds while mounting.

## Rules & gotchas

- **Identity is url and/or key.** An entry needs at least one of `url`/`key`
  (static→url, template→key; both is allowed but unusual). `Write` rejects an item
  with neither, and requires unique non-empty urls and unique non-empty keys. `Open`
  builds two indexes: `byURL` (served over HTTP) and `byKey` (logical lookup).
- **Templates aren't served by the default handler.** A template/parse entry needs
  rendering (data + funcs = host logic); the package can't do it. The default
  handler serves only static — a template (or any non-static, or an absent URL) 404s.
  Render it from a `Middleware` (return `DispatchDone`) or fetch it with `GetByKey`
  and render wherever you like.
- **`nomux` means not routed.** `Handler` takes an entry flagged `nomux` for absent:
  it is not served and the middleware is handed `nil`, whatever its url. `GetByKey`,
  `Bytes` and `Reader` are unaffected — a `nomux` entry is an asset reached by key.
- **mskblob's own flags live in the second byte and demand `nomux`.** The low byte of
  `RestType` (`0x00FF`) mirrors miniskin's item types; the second (`0xFF00`) is
  mskblob's: `Mskblob 0x0100`, `MskBlobAuto 0x0200`. An entry carrying any of them is
  never served, so `Write` requires a **key, no url and `nomux`** — and a blob holding
  one without `nomux` (packed by something else) **does not open**: refused, not read
  as a plain entry.
- **A nested blob is mounted, never served.** An entry marked `mskblob,nomux` holds another
  blob, and `Write` demands it carry a **key and no url** — so it never enters the
  routing index and no handler, present or future, can hand out the container's bytes
  as an asset. `OpenBlob(key)` / `LoadBlob(key, id)` mount it in place over its section
  of the parent: an ordinary `*Blob`, nestable again. The tree shares the root's
  descriptor, so a child's `Close` is a no-op and closing the root turns every later
  read into `ErrClosed`. Nothing in the binary format changes — a container is a normal
  blob whose entry happens to hold one.
- **A blob cannot include itself.** `Write` rejects any item whose `src` is the output
  file, nested or not, before creating anything, so the existing file is left
  untouched. They are compared as files, not as path strings: a relative path or a
  link to the same file is caught too. A blob nested in a *different* output is a
  plain byte copy made at write time, so no cycle can exist in the format.
- **Streaming, not loading.** Static serving uses `io.SectionReader` straight from
  the file — nothing resident in RAM, which is the whole point for a 245 MB asset set.
  Prefer `Reader` over `Bytes` for large entries.
- **Case sensitivity is a blob property.** By default lookups are **case-sensitive**
  (exact match, like miniskin's embedded routes). Build with `nocase` (Options /
  manifest / `manifest`/`create -nocase`) and the blob records a header flag; then
  `GetByURL`/`GetByKey` (and the served Handler) **fold case**. This matters on
  case-sensitive filesystems (Linux), where `Logo.JPG` and `logo.jpg` are distinct
  files — under `nocase` such a pair collides and `Write` rejects it. The stored
  URL/Key keep their original case; only matching folds.
- **GUID is the deploy sync token.** Each `Write` stamps a fresh GUID unless
  `Options.ID` is set. `Load(path, expectID)` rejects a blob whose GUID doesn't
  match, so you can't serve a stale data file against a newer binary. Pin an id (via
  `Options.ID` / `create -id`) for a reproducible, cacheable file. Because a pinned
  id *is* the content's identity, `Options.SkipUnchanged` / `create -skip-unchanged`
  can skip the pack entirely (no `Src` read) when the target already carries that id
  — to force a rebuild, delete the `.blob` or change the id.
- **Hex strings, split low/high.** JSON numbers are float64; large 64-bit sizes /
  offsets / CRCs lose precision or read as meaningless 19-digit integers. So computed
  numbers are hex strings and 64-bit values are split into 32-bit low/high halves
  (like the binary header). They're ignored on input anyway — purely for legible
  `dump`/`list` output.
- **Not updatable in place.** A blob is a build artifact, not a store; rewriting one
  entry would shift every following offset and the data CRC. The edit loop is
  explicit: `dump` → edit → `create`.
- **Sealed index, no traversal.** The runtime never resolves a request against the
  filesystem. A miss is a 404; only packed entries are reachable.
- **Encapsulation.** Build via the manifest helpers, serve via `Handler`/middleware,
  access via `GetBy*`/`Reader`. Internal layout (binary format, indexes, offsets,
  alignment) is private — these signatures are stable across format changes.
