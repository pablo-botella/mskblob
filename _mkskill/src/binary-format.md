---
mkskill:
  pos: 80
  in: readme
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

