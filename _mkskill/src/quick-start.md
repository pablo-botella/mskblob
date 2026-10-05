---
mkskill:
  pos: 40
  in: readme
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

