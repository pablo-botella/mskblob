---
mkskill:
  pos: 60
---

## Build a blob

`create` reads a manifest and packs the bytes. The manifest may be a JSON object `{id?, nocase?, items:[{url, src, [key], [filename], [restype]}]}`, a bare `[ … ]` items array, or a text list of `"<url>\t<src>"` lines.

```
mskblob manifest -dir ./assets -include "*.png,*.jpg" -recurse -base ./assets -o img.json
mskblob create   -manifest img.json -out img.blob -base ./assets
```

Relative `src` resolves against `-base` (default: the manifest's own dir). The guid is taken from `-id`, else the manifest's `id`, else freshly generated.

### Skipping an unchanged rebuild

A pinned id *is* the content's identity, so if the output blob already carries that id the bytes are the same by contract. `-skip-unchanged` makes `create` a **no-op** in that case: it reads only the 128-byte header (no source file is touched) and leaves the file as is.

```
mskblob create -manifest img.json -out img.blob -id 947d88a9-… -skip-unchanged
```

It needs a pinned id; with an auto id every build mints a fresh guid, so there is nothing to compare and the build runs (with a warning). Handy in projects that rebuild many blobs repeatedly. To force a rebuild, delete the `.blob` or change the id.

