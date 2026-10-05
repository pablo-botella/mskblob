---
mkskill:
  pos: 50
  in: readme
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

