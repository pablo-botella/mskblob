# mskblob

Simple blob RO storage, born to give support to alternate storage in https://github.com/pablo-botella/miniskin since v0.3.12
but quite generic so it's just another RO blob.
Covers packing many assets into one external `.blob` sidecar (kept out of the Go binary),

listing/accessing and serving them at runtime, the on-disk format, and the `mskblob` CLI.
Triggers on `.blob` files, the mskblob package/CLI, or "external asset blob / pack file" tasks in Go projects.

```sh
go get github.com/pablo-botella/mskblob                           # library
go install github.com/pablo-botella/mskblob/cmd/mskblob@latest    # CLI
```

- **Zero dependencies** — standard library only.
- **Self-describing** — every entry carries its own identity (url, key, filename,
  size, crc32, type flags); a blob can be inspected, served, and round-tripped on
  its own.
- **GUID sync token** — the header carries a GUID so a loader can confirm a
  deployed `.blob` matches the program that expects it.
- **HTTP-ready** — `Blob.Handler(base)` is a drop-in `http.Handler` with ETag,
  conditional `If-None-Match → 304`, and MIME by extension.
- **Nestable** — an entry can hold another blob, mounted in place with
  `Blob.OpenBlob(key)`: a whole tree of independently built, independently
  verifiable blobs ships as one file.
- **One JSON manifest shape** for create / inspect / dump — human-readable, with
  hex numbers and no float-precision traps.

`mskblob` was **born to give support to alternate storage in miniskin**
([`github.com/pablo-botella/miniskin`](https://pkg.go.dev/github.com/pablo-botella/miniskin)) — the
build-time assembler that *produces* these `.blob` files, and packs assets into
blobs using this component from **v0.3.12** — but it's generic enough to be **just
another blob**, fully standalone: nothing here depends on miniskin.

---

## Table of contents

- [Concepts](#concepts)
- [Quick start](#quick-start)
- [Go API](#go-api)
- [CLI](#cli)
- [Manifest format](#manifest-format)
- [Binary format](#binary-format)
- [Design notes](#design-notes)
- [License](#license)

---

## Concepts

| Term | What it is |
|------|------------|
| **Blob** | One `.blob` file: a 64-byte header, a 64-byte GUID, an index of entries, and the concatenated bytes. Self-describing and immutable. |
| **Item** | One resource — the single unit of both a manifest and a blob's index. Declarative fields (`URL`, `Key`, `Filename`, `RestType`, `Src`) say *what* to pack; `Size`/`CRC32`/`Offset` are computed by the writer. |
| **Manifest** | The interchange format: an optional id plus items. Build a blob from one, or produce one from a blob. The JSON you edit by hand. |
| **GUID** | A random RFC-4122 v4 id stamped in the header. The sync token: a loader can demand the blob's id match what it was built against. |
| **Base** | The URL prefix a blob is mounted under. Entry URLs are stored **relative** to it; `Handler(base)` strips the base before lookup. |
| **Sealed index** | Lookups go through the in-memory index only. A path that isn't a packed entry is a 404 — there is no filesystem access and no traversal. |
| **Nested blob** | An entry whose bytes are themselves a blob (`restype: mskblob,nomux`, key-only). `OpenBlob` mounts it in place over its section of the parent — same format, one level in — so a whole tree of blobs ships as a single file. |

A blob is **not updatable in place**. To change one: `dump` it to a directory
(`-manifest` to get the manifest too), edit, then `create` a fresh blob. It was
never meant to be a database — it's a build artifact.

---

## Quick start

### Build a blob

```go
items := []mskblob.Item{
    {URL: "logo.png", Filename: "logo.png", RestType: mskblob.Static, Src: "assets/logo.png"},
    {URL: "hero.jpg", Filename: "hero.jpg", RestType: mskblob.Static, Src: "assets/hero.jpg"},
}
id, err := mskblob.Write("dist/img.blob", items, mskblob.Options{})
// id is a fresh GUID (pass Options{ID: "..."} to pin one)
```

- `URL` — the lookup key, **relative** to the base the blob is mounted under.
- `Src` — the file on disk to read the bytes from.
- `Filename` — the recorded source name; `Key` — an optional logical key.
- `RestType` — type flags (see below). `Size`/`CRC32`/`Offset` are computed by
  `Write`; any values you set are ignored.

Items are written in lexical `URL` order, so the data region and its CRC are
**reproducible** across runs — only the GUID varies (unless you pin it).

### Load and serve

```go
b, err := mskblob.Load("dist/img.blob", expectedID) // expectedID "" skips the GUID check
if err != nil {
    log.Fatal(err) // for an integral asset set, a missing/mismatched blob is fatal
}
defer b.Close()

mux.Handle("/img/", b.Handler("/img/", nil))   // mount only the base; the blob routes the rest
```

The blob is a **sub-mux**: you register only the base on your own mux and
`Handler` routes everything below it by relative URL. The **default** handler
(`nil` middleware) serves **only static** entries — streamed **lazily** from the
file (nothing resident in RAM, vital for a 245 MB image set), with `ETag` = crc32
(`If-None-Match` → `304`) and a `Content-Type` by extension. Anything else (a
template, or an unknown URL) is treated as if it weren't there → `404`. An entry
flagged `nomux` is never routed at all — reach it by key.

To do more, pass a **middleware** — one hook called for every request with the
matched item (`nil` when the URL is absent, so it can answer unknown routes too).
Its `int` return drives dispatch:

- `mskblob.DispatchAuto` (`0`) — let the blob serve it (static only, as above);
- `mskblob.DispatchDone` (`1`) — *you* already wrote the response (e.g. you
  rendered a template);
- any other value — returned as that HTTP status (`403`, `301`, …).

```go
mux.Handle("/site/", b.Handler("/site/", func(w http.ResponseWriter, r *http.Request, it *mskblob.Item) int {
    switch {
    case it == nil:
        return mskblob.DispatchAuto                 // unknown → 404
    case it.RestType&mskblob.HTMLTemplate != 0:
        render(w, r, it)                            // your rendering
        return mskblob.DispatchDone
    default:
        return mskblob.DispatchAuto                 // static → streamed by the blob
    }
}))
```

The returned value is a plain `http.Handler`, so any router that can mount one
works (chi, gin, std). `Handler` is a building block — *not* a web server (no
listening/TLS/config); that lives in your app (or the `mskblob serve` CLI).

---

## Go API

### Writing

```go
func Write(path string, items []Item, opts Options) (id string, err error)
func NewID() (string, error)   // a random RFC-4122 v4 GUID

type Options struct {
    ID            string // guid to stamp; a fresh v4 GUID is generated when empty
    NoCase        bool   // record the blob as case-insensitive (URL/Key lookups fold case)
    SkipUnchanged bool   // with a pinned ID, skip the write (no Src read) when path already holds that id
}
```

### Opening

```go
func Open(path string) (*Blob, error)              // open + read index into memory
func Load(path, expectID string) (*Blob, error)    // Open + verify GUID when expectID != ""
func ReadHeader(path string) (Header, error)       // cheap: 128-byte header only

type Header struct {
    Version   uint8
    ID        string
    Count     uint32
    DataCRC32 uint32
    DataSize  int64
}
```

`ReadHeader` is the cheap path for cache checks: it reads only the 128-byte header
(magic, version, count, data crc, and the GUID) without touching the index or
data — enough to confirm a deployed file matches the program via its GUID.

### Nested blobs

A blob's bytes are opaque, so an entry can hold another blob. Mark it
`Mskblob | Nomux` and give it a **key and no URL** — `Write` enforces all three —
and it is mounted instead of served:

```go
func (b *Blob) OpenBlob(key string) (*Blob, error)            // mount the nested blob
func (b *Blob) LoadBlob(key, expectID string) (*Blob, error)  // OpenBlob + verify the GUID

var ErrClosed = errors.New("mskblob: blob is closed")
```

What comes back is an ordinary `*Blob`, read **in place** over its own section of
the parent — nothing is extracted — so `OpenBlob` works on it again, at any depth.
Each level keeps its own GUID, index and case rule, and its offsets stay relative
to itself: composing levels is one addition each, resolved when mounting and not
on every read.

The whole tree reads through the descriptor the root opened. A nested blob's
`Close` is therefore a no-op — it owns nothing — while closing the root closes the
subtree with it: a later read returns `ErrClosed` instead of reaching a freed
descriptor.

Composing one is the usual build, bottom up — write each inner blob as a file,
then pack it like any other source:

```go
mskblob.Write("dist/site.blob", []mskblob.Item{
    {Key: "/img", Filename: "img.blob", RestType: mskblob.Mskblob | mskblob.Nomux, Src: "dist/img.blob"},
}, mskblob.Options{})
```

A blob cannot include itself: `Write` rejects any item, nested or not, whose `Src`
is the output file. The two are compared as files rather than as path strings, and
the check runs before anything is created, so the existing file is left untouched.

### Listing & access

The package's job is to **list** what's inside and hand you the bytes — it finds
resources for you so you never walk the index yourself. An entry is identified by
its **URL** (how it's served) and/or its **Key** (a logical id), which may differ:
`static` resources have a URL; `template`/`parse` resources usually have only a Key
and aren't served as a route.

```go
func (b *Blob) Items() []Item                         // list everything
func (b *Blob) GetByURL(url string) *Item             // lookup by URL  (nil if absent)
func (b *Blob) GetByKey(key string) *Item             // lookup by Key  (nil if absent — how templates are reached)
func (b *Blob) Bytes(it *Item) ([]byte, error)        // read an entry whole
func (b *Blob) Reader(it *Item) *io.SectionReader     // stream an entry (nothing resident — use io.Copy)
func (b *Blob) Header() Header
func (b *Blob) Manifest() Manifest                     // header + items as the interchange struct
func (b *Blob) Close() error
```

`GetBy*` return a read-only `*Item` handle (URL, Key, Filename, RestType, Size,
…); internal layout (offsets, alignment) is never exposed. Lookups are pure
in-memory map reads — they can't fail, so there's no error to check, just `nil`.

### Serving

```go
const (
    DispatchAuto = 0 // let the blob serve it (a static entry; anything else → 404)
    DispatchDone = 1 // the middleware already wrote the whole response
    // any other int → returned as that HTTP status
)
type Middleware func(w http.ResponseWriter, r *http.Request, it *Item) int

func (b *Blob) Handler(base string, mw Middleware) http.Handler
func MimeByExt(name string) string   // extension → Content-Type
```

`Handler` is the sub-mux described above: a `nil` middleware serves only static
entries; a middleware sees every request (item `nil` if absent) and its return
drives dispatch. An entry flagged `Nomux` is **not routed**: for the handler and for
the middleware alike it counts as absent, and stays reachable by key. It is the **only** part touching `net/http`; everything else is
router-agnostic, so you can also ignore `Handler` and build serving yourself from
`GetByURL`/`Reader`.

### Item & resource-type flags

```go
type Item struct {
    URL      string   // lookup key, relative to the mount base
    Key      string   // optional logical key
    Filename string   // recorded source name
    RestType RestType // type flags
    Src      string   // build-time source path (ignored at runtime)
    Size     uint64   // computed by Write
    CRC32    uint32   // computed by Write (also the ETag)
    Offset   uint64   // computed by Write
}

type RestType int32
const (
    Static       RestType = 0x0001
    HTMLTemplate RestType = 0x0002
    Parse        RestType = 0x0004
    Response     RestType = 0x0008
    Nomux        RestType = 0x0010
    // low byte: miniskin's flags; second byte: mskblob's own
    Mskblob      RestType = 0x0100 // the entry's bytes are themselves a blob
    MskBlobAuto  RestType = 0x0200 // the entry is the blob's self-contained server configuration
)

func (r RestType) Names() string  // "static,parse"  (the JSON form)
func (r RestType) String() string // "0x00000005"    (hex form)
```

`RestType` mirrors miniskin's item `type=` flags so a blob built by miniskin
preserves how each resource should be wired. For standalone use you typically only
need `Static`. `Mskblob` is the one flag that isn't miniskin's: it marks a
[nested blob](#nested-blobs), mounted with `OpenBlob` rather than served.

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
mskblob generate-claude-skill                  [-dst f] [-global] [-force]
mskblob generate-agent-docs                    [-dst f] [-force]
```

> **Every option is a named flag — order never matters.** If you forget one, add it
> at the end. There are no positional arguments, so nothing is silently swallowed: an
> unknown flag or a stray argument is a clear error, and a command run with no flags
> prints its own help.

> `SKILL.md` and `AGENTS.md` are **generated** from the `ai/` sources (a template
> plus `ai/core/*.md`), the same model miniskin uses — edit the sources, not the
> generated files, then re-run the two `generate-*` commands. `generate-claude-skill`
> writes the project-local skill by default; `-global` installs it into
> `~/.claude/skills/mskblob/SKILL.md` (available from every project, no elevation —
> it's under your own home), mutually exclusive with `-dst`.

### `info` — header at a glance

```
$ mskblob info -blob img.blob
blob:    img.blob
version: 1
id:      947d88a9-7196-490c-87dd-9a8a1262b0ec
entries: 2
nocase:  false
dataCRC: 0x6d38580c
data:    18 bytes

$ mskblob info -blob img.blob -short
947d88a9-7196-490c-87dd-9a8a1262b0ec  2 entries  18 bytes  nocase=false
```

### `list` — the contents

Prints a GitHub-style markdown document (a header section plus a padded items
table) by default.

```
$ mskblob list -blob img.blob
## blob

- id: `947d88a9-7196-490c-87dd-9a8a1262b0ec`
- version: 1
- entries: 2
- nocase: false
- dataCRC: 0x6d38580c
- data: 18 bytes

## items

| URL          | SIZE | CRC32      | TYPE   | FILENAME     |
|--------------|-----:|------------|--------|--------------|
| app.css      |   15 | 0x034CA4BD | static | app.css      |
| img/logo.png |    3 | 0x15BF411A | static | img/logo.png |
```

Output flags:

- `-json` — emit the manifest JSON instead of the markdown table.
- `-md` — emit the markdown table (the default; explicit for clarity). Mutually
  exclusive with `-json`.
- `-o <file>` — write to that file instead of stdout (same meaning as `manifest -o`).
  Without `-o` the output goes to stdout, so you can view it or redirect it with `>`.

### `manifest` — scan a directory into an editable manifest

`create` builds *from a manifest*, so the usual flow is: generate one from a
directory, **review and tweak the flags by hand**, then build.

```sh
# scan ./assets/prod-img for images, src relative to ./assets, write a manifest:
mskblob manifest -dir ./assets/prod-img -base ./assets -include "*.jpg,*.png" -recurse -o prod-img.json
```

- does **not** recurse unless `-recurse`;
- `-include` / `-exclude` are comma-separated globs matched on the file name;
- `-nocase` makes the matching case-insensitive (`*.jpg` matches `.JPG`) **and**
  marks the resulting blob case-insensitive — recorded in its header, so runtime
  lookups fold case (important on Linux, where `Logo.JPG` ≠ `logo.jpg`);
- `-base` makes `src` relative to a chosen root so the manifest is portable
  (default: the scanned dir);
- `-o` writes to a file (default: stdout).

Every item comes out as `restype: "static"` — edit them before building.

### `create` — build a blob

```sh
mskblob create -manifest prod-img.json -out prod-img.blob -base ./assets
```

- the manifest may be the full JSON object `{id?, items:[...]}`, a bare `[ … ]`
  items array, or a plain text list (one `"<url>\t<src>"` — or just `"<src>"` —
  per line; `#` comments and blank lines ignored);
- relative `src` paths resolve against `-base` (default: the manifest's own
  directory);
- the GUID is taken from `-id`, else the manifest's `id`, else freshly generated;
- missing `filename` defaults to `url`; missing/zero `restype` defaults to `static`;
- `-skip-unchanged` makes `create` a no-op when the output blob already exists with
  the **pinned** id — a pinned id is the content's identity, so no source file is
  read (just the 128-byte header) and the file is left untouched. It needs a pinned
  id; with an auto id every build mints a fresh guid, so there's nothing to compare
  and the build runs. Useful when a project rebuilds many blobs repeatedly; to force
  a rebuild, delete the `.blob` or change the id.

A **nested blob** takes no special flag — it is an item like any other, with a `key`,
no `url` and a built `.blob` as its source. Levels are built bottom up, one `create`
per level:

```sh
mskblob create -manifest img.json  -out img.blob    # the inner blob
# site.json: [{"key": "/img", "filename": "img.blob", "restype": "mskblob,nomux", "src": "img.blob"}]
mskblob create -manifest site.json -out site.blob   # the container
```

### `dump` — extract a blob's files

```sh
mskblob dump -blob img.blob -baseout ./out                            # ./out/<files>
mskblob dump -blob img.blob -baseout ./out -manifest ./out/manifest.json  # + the manifest
mskblob create -manifest ./out/manifest.json -out img2.blob          # round-trip
```

`dump` is for **extracting the asset files**: it writes every entry under `-baseout`
(its base directory), each at its url — or at its **key** when it has no url, as a
template or a nested blob does. It does **not** write a manifest by default —
`-manifest <file>` is an extra that also emits the manifest in one command, with each
`src` pointing at the just-written files, so `create -manifest <file>` recreates the
blob. Entry paths are validated — an entry that would escape the base dir (`..`,
absolute) or has no name at all is rejected.

**One entry at a time.** `dump -file <key>` extracts just the entry with that **key**
to exactly one destination:

```sh
mskblob dump -blob img.blob -file /patata/frita.png -baseout ./out  # ./out/patata/frita.png
mskblob dump -blob img.blob -file /patata/frita.png -out frita.png  # ./frita.png (cwd-relative)
mskblob dump -blob img.blob -file /patata/frita.png -out ./imgs/    # ./imgs/frita.png (dir → basename)
mskblob dump -blob img.blob -file /patata/frita.png -stdout         # bytes to stdout
```

`-baseout` keeps the entry at its subpath; `-out` is an explicit path (a directory if
it ends in a separator, then the basename is appended); `-stdout` writes the raw bytes.
`-out`/`-stdout` apply only with `-file`, and a missing or duplicate destination is an
error.

### `serve` — run a web server from a JSON config

The standalone server, driven by one JSON file. It mounts one or more blobs, each
under its own base; static entries stream lazily and **template** entries are
rendered with the configured variables. Variables and extra headers exist at two
levels — **global** and **per-blob** — and merge, with the blob's winning. Serves
HTTPS when `tls.cert`/`tls.key` are set (or plain HTTP behind a Cloudflare-style
tunnel).

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

`tls`, `vars`, `headers`, and per-blob `id`/`vars`/`headers` are all optional;
`addr` defaults to `:8080` and `base` to `/`. This is also the reference for how
`Blob.Handler` is wired with a middleware — your own app does the same shape.

**Serving a blob nested in another.** A [nested blob](#nested-blobs) is never served
through its parent: it has no url, and `serve` mounts nothing by itself. Name it —
`internal_path` lists the **keys to descend through**, outermost first, one nested
blob per element:

```json
{
  "blobs": [
    { "file": "site.blob", "base": "/" },
    { "file": "site.blob", "base": "/docs/",    "internal_path": ["/docs"] },
    { "file": "site.blob", "base": "/docs/es/", "internal_path": ["/docs", "/es"], "id": "947d…" }
  ]
}
```

It is an array rather than one slash-separated string because a key may contain
slashes of its own. Absent or empty, the entry mounts the file itself, as before. The
mounted blob is read in place, straight from its section of the file — nothing is
extracted — and a file listed by several entries is opened once. `id` checks the
blob **finally mounted**, not the file that contains it. A key that does not exist,
or is not a nested blob, stops the server from starting.

**What a URL that is not an entry may still find.** By default a mount serves exact
matches only: `/` and `/docs/` are a 404, because no entry has that url. Two options,
**per mount** and off unless declared, say what else to try — the lists are tried in
order and the first hit wins:

```json
{ "file": "site.blob", "base": "/",
  "remove_extensions": [".html"],
  "default_document": ["index.html"] }
```

| Option | Applies when the URL… | Tries | Example |
|---|---|---|---|
| `remove_extensions` | has no extension, or ends in `/` | the URL (minus its slash) + each extension | `/about` → `about.html` |
| `default_document` | is the root, or ends in `/` | the URL + each name | `/docs/` → `docs/index.html` |

An entry by the exact URL always wins, and is served as it is — there are **no
redirects**: `/about.html` keeps working next to `/about`. When both options apply
the extensions go first (`/docs/` finds `docs.html` before `docs/index.html`). Nothing
else is guessed: `/docs` without its slash is not the folder and stays a 404 — what
is not declared does not exist. A `nomux` entry is never found this way, and a
template found this way is rendered like any other.

**HTTPS and an encrypted key.** `tls.cert` and `tls.key` are PEM files. The key may
be **encrypted** — PKCS#8 with PBKDF2 and AES or triple-DES, what `openssl` writes by default
(`BEGIN ENCRYPTED PRIVATE KEY`). Its password is never in the config, only where to
find it:

```json
"tls": { "cert": "cert.pem", "key": "key.pem",
         "password_file": "/run/secrets/tls_password",
         "password_env": "MSKBLOB_TLS_PASSWORD" }
```

`password_file` is a file holding the password (a trailing newline is dropped) — a
mounted Docker/Kubernetes secret fits; `password_env` names an environment variable.
With both, the file wins, and the variable is used when the file is not there. An
unencrypted key needs neither. A key derived with scrypt, a legacy `DEK-Info` PEM or
a PFX is refused with a message saying so.

**Self-contained: the blob carries its own config.** With `-config` the config is the
entry point and names the blobs to open. `-auto` turns it round — the blob is the
entry point and brings the config that serves it, like a disk with its `autoexec.bat`:

```sh
mskblob serve -auto site.blob
```

The config is an ordinary entry of the blob at the fixed key
**`/mskblob/auto/site.json`**, flagged `auto,nomux` and with no url, so it is never
served itself:

```json
{ "key": "/mskblob/auto/site.json", "restype": "auto,nomux", "src": "site.json" }
```

Its content is the same JSON as above, with one difference: `file` may be left out,
meaning **this same blob** — the config cannot know what the file will be called once
deployed. `internal_path` then descends from it, and a `file` with a value is still an
external file:

```json
{
  "blobs": [
    { "base": "/" },
    { "base": "/docs/", "internal_path": ["/docs"] }
  ]
}
```

If the entry is missing, lacks `auto` or `nomux`, has a url, or is not valid JSON, the
server does not start. `-auto` and `-config` cannot be combined — nothing is merged:
to serve the blob another way, pass a config file and the one inside is not looked at.
The config of a nested blob is ignored when its parent is served.

With `-auto` the certificate travels in the blob too. `tls.cert` and `tls.key` are
then not paths but **keys of entries of the blob itself**, which must live under
`/mskblob/` and be flagged `auto,nomux` — without those attributes they cannot be
used, and the server does not start:

```json
{ "key": "/mskblob/auto/cert.pem", "restype": "auto,nomux", "src": "cert.pem" },
{ "key": "/mskblob/auto/key.pem",  "restype": "auto,nomux", "src": "key.pem" }
```

```json
"tls": { "cert": "/mskblob/auto/cert.pem", "key": "/mskblob/auto/key.pem",
         "password_env": "MSKBLOB_TLS_PASSWORD" }
```

Being `nomux` and url-less they are never served. But the blob now **contains the
private key**, and `dump` extracts it like any entry: encrypt the key, so that the
file alone is not enough, and keep its password outside — in the environment or in a
file. To use a certificate on disk instead (say, one renewed without rebuilding the
blob), serve with `-config`.

---

## Manifest format

One JSON shape is used everywhere — `create` reads it; `manifest`, `dump` and
`list` produce it:

```json
{
  "id": "947d88a9-7196-490c-87dd-9a8a1262b0ec",
  "items": [
    {
      "url": "img/logo.png",
      "key": "/img/logo.png",
      "filename": "logo.png",
      "restype": "static",
      "src": "assets/logo.png",
      "crc32": "0x15BF411A",
      "sizeLow": "0x00000003",
      "sizeHigh": "0x00000000",
      "offsetLow": "0x000000E0",
      "offsetHigh": "0x00000000"
    }
  ]
}
```

**Declarative fields** — what to pack (read on `create`):

| Field | Required | Meaning |
|-------|----------|---------|
| `url` | one of `url`/`key` | lookup key, relative to the mount base |
| `src` | yes (build) | file to read the bytes from |
| `key` | one of `url`/`key` | logical key — how a template or a nested blob is reached |
| `filename` | no | recorded source name (defaults to `url`) |
| `restype` | no | type-flag mask (defaults to `static`) |

**`restype`** is a human flag mask: comma-separated names
`static,tpl,parse,rsp,nomux,mskblob,auto` (empty = none). On input a hex or decimal value
(`"0x05"`, `5`) is also accepted, but the canonical output is names.

**A nested blob** is declared like any other entry — `restype: "mskblob,nomux"`, a
`key`, no `url` (it is mounted, never served), and `src` pointing at the already-built
`.blob`:

```json
{ "key": "/img", "filename": "img.blob", "restype": "mskblob,nomux", "src": "dist/img.blob" }
```

**`mskblob` and `auto` are mskblob's own flags**, and whatever carries one is never
served: the entry must have a `key`, no `url`, and `nomux` as well. `create` refuses
to build the blob otherwise. The blob's **self-contained server configuration** (see
`serve -auto`) is one such entry, at a fixed key:

```json
{ "key": "/mskblob/auto/site.json", "restype": "auto,nomux", "src": "site.json" }
```

The folder `/mskblob/` is where the server reads what it needs from the blob itself:
that configuration and, for HTTPS, the certificate and its key. Each such entry must
be under `/mskblob/` **and** flagged `auto,nomux`, or the server will not use it.

Each level has its own manifest: build the inner blobs first, then reference them
from the one above.

**Computed fields** — `crc32`, `sizeLow`/`sizeHigh`, `offsetLow`/`offsetHigh`
(plus the manifest-level `version`, `count`, `dataCRC32`). They are emitted for
inspection and **ignored on `create`** (always recomputed from the actual bytes),
so you never have to keep them in sync by hand. They are fixed-width 32-bit hex
strings; the 64-bit `size`/`offset` are split into low/high halves — see
[Design notes](#design-notes) for why.

---

## Binary format

All integers little-endian.

```
BLOCK A — header (64 bytes)
  0   magic       "MSPK"
  4   version     uint8
  5   flags       uint8   (bit 0x01 = case-insensitive URL/Key lookup)
  6   reserved    [2]byte
  8   count       uint32  number of index entries
  12  dataCRC     uint32  crc32(IEEE) of block D
  16  dataOffset  uint32  absolute offset where block D begins
  20  reserved    pad to 64

BLOCK B — id (64 bytes)
  guid ASCII, zero-padded — the sync token

BLOCK C — index (count entries, each padded to a 16-byte boundary)
  entrysize uint32   total bytes of this entry (incl. itself + pad)
  key       string\0 asset key
  size      uint64   data byte length
  crc32     uint32   crc32 of the data (also the ETag)
  restype   int32    resource-type flags (Static, Parse, …)
  url       string\0 http route, relative to the base
  filename  string\0 source filename
  pad       \0…      to the next 16-byte boundary

BLOCK D — data
  concatenated entry bytes at dataOffset; each entry's offset is the
  running sum of the preceding sizes
```

Index entries are 16-byte aligned the way Windows resources are — it keeps the
index scannable and leaves room without disturbing offsets. Because every entry
carries its full identity, the blob is self-describing: it can be served on its
own, and its header alone is enough to verify a deployed file (via the GUID).

Every position it records is relative to the blob's own start — `dataOffset` to
byte 0 of the file, each entry's offset to `dataOffset`. Nothing absolute is ever
baked in, which is why a blob stays valid wherever it lands: nesting one inside
another copies it byte for byte, and mounting it is a single addition, with no
relocation to patch.

---

## Design notes

**Why a GUID, and why pinning matters.** Each `Write` stamps a fresh GUID unless
`Options.ID` is set. The GUID is a content-deploy sync token: `Load(path,
expectID)` rejects a blob whose GUID doesn't match, so you can't accidentally
serve last week's data file against this week's binary. Build pipelines that want a
stable id across rebuilds (e.g. a cache key) pin one with `Options.ID` / `create
-id`; everything else in a blob is reproducible, so a pinned id makes the whole
file reproducible. And because a pinned id *is* the content's identity, `Write` can
short-circuit: with `Options.SkipUnchanged` (or `create -skip-unchanged`) a target
already carrying that id is left untouched and no `Src` is read — `Load`/`ReadHeader`
*check* the id, `SkipUnchanged` *acts* on that check to spare a needless pack.

**Why hex strings, split low/high.** JSON numbers are float64 — large 64-bit sizes,
offsets and CRCs lose precision or render as unreadable 19-digit integers that
*mean nothing* to a human. So computed numbers are hex strings (`0x15BF411A`), and
64-bit values are split into 32-bit `low`/`high` halves, exactly like the binary
header. A CRC reads as a CRC; a size over 4 GiB is still exact. Since these fields
are ignored on input anyway, this is purely about making `dump`/`list` output
legible.

**Why not updatable.** A blob is a build artifact, not a store. Rewriting one entry
would mean shifting every following offset and recomputing the data CRC — i.e.
rewriting the file. So the supported edit loop is explicit: `dump` → edit →
`create`. This keeps the format dead simple and the reader trivially safe.

**Sealed index, no traversal.** The runtime never resolves a request against the
filesystem. `Handler` looks the (base-stripped) path up in the in-memory index; a
miss is a 404. There is no way to reach a path that wasn't packed.

---

## License

MIT.
