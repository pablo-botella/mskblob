---
mkskill:
  pos: 230
  in: ai*
---

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
