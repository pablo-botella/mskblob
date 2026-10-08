---
mkskill:
  pos: 85
  in: readme
---

## C and Harbour readers

`capi/` holds a dependency-free reader for the format in plain C (C99 / C11,
MSVC or gcc, 32 or 64 bit), a small command-line tool built on it, and a
Harbour wrapper that exposes the reader as Harbour functions. They read a
blob through a file mapping and give the same answers as the Go package:
header, entries, exact and wildcard lookup, bounded sequential reads, crc32
checks, extraction and nested blobs.

They are consumers only, on purpose. There is no interest in a C or Harbour
*writer*: the Go package and the `mskblob` CLI already build blobs and run
anywhere, so the build step stays in Go. What other languages need is to
*open* what Go produced — a 32-bit C program, a Harbour program — and that
is what `capi/` does.

```
mskblob-c info|list [pattern]|verify|dump <outdir>|get <key> [outfile] <file.blob>
```

```xbase
hBlob := MskBlobOpen( "runtime.blob" )
aItem := MskBlobFindFirst( hBlob, "*.msi" )
DO WHILE aItem != NIL
   cChunk := MskBlobReadBytes( hBlob, aItem, 65536 )   // like FRead, bounded to the entry
   ...
   aItem := MskBlobFindNext( hBlob )
ENDDO
```

Build and API details are in [`capi/README.md`](capi/README.md); the format
is fully described in `mskblob.h`.

---

