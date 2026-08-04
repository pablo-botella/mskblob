---
mkskill:
  pos: 50
---

## Inspect a blob

A blob is fully self-describing, so these read only the file you point at — no source tree needed.

```
mskblob info -blob img.blob            # header: id, entries, dataCRC, size, nocase
mskblob info -blob img.blob -short     # one line
mskblob list -blob img.blob            # header + items table (markdown) to stdout
mskblob list -blob img.blob -json      # JSON to stdout (redirect with > if you want)
mskblob list -blob img.blob -json -o out.json   # JSON written to a file
```

