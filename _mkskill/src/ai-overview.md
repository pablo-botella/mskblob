---
mkskill:
  pos: 200
  in: ai*
---

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
