---
mkskill:
  pos: 210
  in: ai*
---

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
