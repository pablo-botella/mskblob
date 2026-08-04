---
mkskill:
  pos: 70
---

## Edit a blob (dump → edit → create)

A blob is **not updatable in place** — rewriting one entry would shift every following offset and the data CRC. The edit loop is explicit:

```
mskblob dump   -blob img.blob -baseout ./out -manifest ./out/manifest.json
# …edit files and/or ./out/manifest.json…
mskblob create -manifest ./out/manifest.json -out img2.blob
```

`dump` extracts the asset files into `-baseout`; pass `-manifest <file>` to also get the manifest, whose `src` points at the just-written files, so `create -manifest <file>` re-creates the blob (no `-dir` needed). Entry paths are validated — an entry that would escape the base dir (`..`, absolute) is rejected.

### Extract a single entry

`dump -file <key>` pulls just the entry with that **key** to one destination:

```
mskblob dump -blob img.blob -file /patata/frita.png -baseout ./out  # ./out/patata/frita.png
mskblob dump -blob img.blob -file /patata/frita.png -out frita.png  # ./frita.png (cwd-relative)
mskblob dump -blob img.blob -file /patata/frita.png -out ./imgs/    # ./imgs/frita.png (dir → basename)
mskblob dump -blob img.blob -file /patata/frita.png -stdout         # raw bytes to stdout
```

`-baseout` keeps the entry at its subpath; `-out` is an explicit path (a directory if it ends in a separator, then the basename is appended); `-stdout` writes the raw bytes. `-out`/`-stdout` apply only with `-file`, and a missing or duplicate destination is an error.

