# mskblob/capi

Reader for [mskblob](https://github.com/pablo-botella/mskblob) files in plain C,
plus a command-line tool. No dependencies beyond the C standard library and the
system (MapViewOfFile on Windows, mmap elsewhere): builds with MSVC, MinGW, gcc
or clang, 32 or 64 bits, and the two source files drop into any program that
has to open a blob without Go.

The file is memory-mapped read-only and parsed in place: nothing is copied,
entry bytes are read straight from the mapping, and a nested blob is opened
over its entry's byte range.

- `mskblob.h` / `mskblob.c` — the reader (open a file, parse bytes in memory,
  nested blobs, lookups, findfirst/findnext/findclose, readbytes, crc32
  verification, extract).
- `mskblob_cli.c` — the `mskblob-c` tool.
- `hbmskblob.c` / `mskblob.ch` / `hbmskblob.hbp` — the same reader as Harbour
  functions (see below).

## Build

```bat
build.bat          :: MSVC, 32-bit, static CRT (runs on any Windows)
build.bat x64      :: MSVC, 64-bit
make               :: gcc / MinGW / clang
```

## Tool

```
mskblob-c info   <file.blob>                 header: id, version, entries, nocase, data crc and size
mskblob-c list   <file.blob> [pattern]       header plus the entries, all or those matching * ? on url/key
mskblob-c verify <file.blob>                 check every crc32, nested blobs included
mskblob-c dump   <file.blob> <outdir>        extract every entry under outdir, by url (or key)
mskblob-c get    <file.blob> <key> [outfile] extract one entry (by key, else by url) to outfile or stdout
```

Exit code is 0 on success, 1 on any failure, 2 on usage errors. Output is plain
text.

## Library

```c
#include "mskblob.h"

mskblob *b;
if (mskblob_open("runtime.blob", &b) != MSKBLOB_OK) ...   /* or mskblob_parse(bytes, len, &b) */

mskblob_find f;
for (int rc = mskblob_findfirst(b, "*.msi", &f); rc == MSKBLOB_OK; rc = mskblob_findnext(&f)) {
    const mskblob_item *it = f.item;                 /* key, url, filename, size, crc32, restype */
    char buf[4096];
    size_t n;
    while (mskblob_readbytes(b, it, buf, sizeof buf, &n) == MSKBLOB_OK && n > 0)
        ...                                          /* like ReadFile, bounded to the entry */
}
mskblob_findclose(&f);
mskblob_close(b);
```

Functions:

| | |
|---|---|
| `mskblob_open(path, &b)` | maps the file read-only and parses it in place |
| `mskblob_parse(data, len, &b)` | reader over bytes already in memory (not copied) |
| `mskblob_open_nested(parent, item, &b)` | parses an entry flagged `mskblob` as a blob of its own; close it before the parent |
| `mskblob_close(b)` | |
| `mskblob_hdr(b)`, `mskblob_count(b)`, `mskblob_at(b, i)` | header and entries by index |
| `mskblob_find_key(b, key)`, `mskblob_find_url(b, url)` | exact lookups, folding case when the blob is `nocase` |
| `mskblob_findfirst(b, pattern, &f)`, `mskblob_findnext(&f)`, `mskblob_findclose(&f)` | enumeration with `*` and `?` on the entry name (url, or key when it has none) |
| `mskblob_readbytes(b, item, buf, len, &got)`, `mskblob_readseek(b, item, pos)` | sequential reads bounded to the entry, like `ReadFile`; the blob keeps one read position |
| `mskblob_read(b, item, offset, buf, len)` | copies a slice of the entry at an explicit offset |
| `mskblob_readall(b, item, &buf, &len)` | verifies and returns the whole entry as a malloc'd, NUL-terminated copy |
| `mskblob_data(b, item)` | pointer to the entry's bytes inside the mapping |
| `mskblob_verify(b, item)`, `mskblob_verify_all(b)` | crc32 of one entry / of the whole data region |
| `mskblob_extract(b, item, path)` | verifies and writes the entry to a file |
| `mskblob_restype_names(restype, buf)` | `"static,parse"` style names |
| `mskblob_strerror(err)` | |
| `mskblob_crc32(crc, data, len)` | IEEE crc32, for callers that stream |

Every function returns `MSKBLOB_OK` (0) or a negative `mskblob_error`.

## Harbour

`hbmskblob.c` wraps the reader for Harbour; it is plain C against `hbapi.h`
and builds with `hbmk2 hbmskblob.hbp` into a static library, or straight into
a program by listing `mskblob.c` and `hbmskblob.c` in its `.hbp`.
`test_hbmskblob.hbp` builds a test that lists a blob and checks every entry.

```xbase
#include "mskblob.ch"

LOCAL hBlob := MskBlobOpen( "runtime.blob" ), aItem, cChunk
IF hBlob != NIL
   aItem := MskBlobFindFirst( hBlob, "*.msi" )
   DO WHILE aItem != NIL
      DO WHILE Len( cChunk := MskBlobReadBytes( hBlob, aItem, 65536 ) ) > 0
         ...                                   // like FRead, bounded to the entry
      ENDDO
      aItem := MskBlobFindNext( hBlob )
   ENDDO
   MskBlobFindClose( hBlob )
   MskBlobClose( hBlob )                       // the garbage collector closes it too
ENDIF
```

| | |
|---|---|
| `MskBlobOpen( cPath )` | handle, or NIL |
| `MskBlobOpenNested( hBlob, aItem )` | handle over a nested entry, or NIL; it owns a verified copy, so parent and child close in any order |
| `MskBlobClose( hBlob )`, `MskBlobError( hBlob )`, `MskBlobErrorStr( nError )` | |
| `MskBlobHeader( hBlob )` | `{ id, version, count, nocase, dataCRC, dataSize }` (`MSKBLOB_HDR_*`) |
| `MskBlobFindFirst( hBlob, [cPattern] )`, `MskBlobFindNext( hBlob )`, `MskBlobFindClose( hBlob )` | item arrays, NIL when there is nothing (more) |
| `MskBlobFind( hBlob, cKeyOrUrl )` | exact lookup, or NIL |
| `MskBlobReadBytes( hBlob, aItem, nLen )`, `MskBlobReadSeek( hBlob, aItem, nPos )` | sequential reads bounded to the entry, `""` at the end |
| `MskBlobReadAll( hBlob, aItem )` | the whole entry, verified, or NIL |
| `MskBlobVerify( hBlob, aItem )`, `MskBlobVerifyAll( hBlob )`, `MskBlobExtract( hBlob, aItem, cPath )` | logical |
| `MskBlobRestypeNames( nRestype )` | `"static,parse"` |

An item array is `{ key, url, filename, size, offset, crc32, restype }`,
addressed with the `MSKBLOB_ITEM_*` constants of `mskblob.ch`. Handles are
garbage collected: a blob left open is closed when its last reference goes,
and a closed handle passed to any function raises a runtime error instead of
touching freed memory. Item arrays are checked against the blob's data region
before use, so an edited array gets `MSKBLOB_E_ARGUMENT`, never a crash.

## Format

Little-endian. Block A, 64-byte header: magic `MSPK`, version (1), flags
(bit 0 = nocase), entry count, crc32 of the data region, offset of the data
region. Block B, 64 bytes: the blob's GUID, zero-padded. Block C, the index:
one record per entry, padded to 16 bytes, holding the record size, key,
size, crc32, resource-type flags, url and filename. Block D: the entries'
bytes, concatenated, uncompressed, in index order. The full description is in
`mskblob.h` and in the Go package.

## License

MIT, see `LICENSE`.
