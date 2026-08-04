---
mkskill:
  pos: 10
  in: readme
---

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
- **One JSON manifest shape** for create / inspect / dump — human-readable, with
  hex numbers and no float-precision traps.

`mskblob` was **born to give support to alternate storage in miniskin**
([`github.com/pablo-botella/miniskin`](https://pkg.go.dev/github.com/pablo-botella/miniskin)) — the
build-time assembler that *produces* these `.blob` files, and packs assets into
blobs using this component from **v0.3.12** — but it's generic enough to be **just
another blob**, fully standalone: nothing here depends on miniskin.

---

