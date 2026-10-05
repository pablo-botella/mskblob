---
mkskill:
  pos: 70
  in: readme
---

## Manifest format

One JSON shape is used everywhere — `create` reads it; `manifest`, `dump` and
`list` produce it:

```json
{
  "id": "947d88a9-7196-490c-87dd-9a8a1262b0ec",
  "items": [
    {
      "url": "img/logo.png",
      "key": "/img/logo.png",
      "filename": "logo.png",
      "restype": "static",
      "src": "assets/logo.png",
      "crc32": "0x15BF411A",
      "sizeLow": "0x00000003",
      "sizeHigh": "0x00000000",
      "offsetLow": "0x000000E0",
      "offsetHigh": "0x00000000"
    }
  ]
}
```

**Declarative fields** — what to pack (read on `create`):

| Field | Required | Meaning |
|-------|----------|---------|
| `url` | one of `url`/`key` | lookup key, relative to the mount base |
| `src` | yes (build) | file to read the bytes from |
| `key` | one of `url`/`key` | logical key — how a template or a nested blob is reached |
| `filename` | no | recorded source name (defaults to `url`) |
| `restype` | no | type-flag mask (defaults to `static`) |

**`restype`** is a human flag mask: comma-separated names
`static,tpl,parse,rsp,nomux,mskblob,auto` (empty = none). On input a hex or decimal value
(`"0x05"`, `5`) is also accepted, but the canonical output is names.

**A nested blob** is declared like any other entry — `restype: "mskblob,nomux"`, a
`key`, no `url` (it is mounted, never served), and `src` pointing at the already-built
`.blob`:

```json
{ "key": "/img", "filename": "img.blob", "restype": "mskblob,nomux", "src": "dist/img.blob" }
```

**`mskblob` and `auto` are mskblob's own flags**, and whatever carries one is never
served: the entry must have a `key`, no `url`, and `nomux` as well. `create` refuses
to build the blob otherwise. The blob's **self-contained server configuration** (see
`serve -auto`) is one such entry, at a fixed key:

```json
{ "key": "/mskblob/auto/site.json", "restype": "auto,nomux", "src": "site.json" }
```

The folder `/mskblob/` is where the server reads what it needs from the blob itself:
that configuration and, for HTTPS, the certificate and its key. Each such entry must
be under `/mskblob/` **and** flagged `auto,nomux`, or the server will not use it.

Each level has its own manifest: build the inner blobs first, then reference them
from the one above.

**Computed fields** — `crc32`, `sizeLow`/`sizeHigh`, `offsetLow`/`offsetHigh`
(plus the manifest-level `version`, `count`, `dataCRC32`). They are emitted for
inspection and **ignored on `create`** (always recomputed from the actual bytes),
so you never have to keep them in sync by hand. They are fixed-width 32-bit hex
strings; the 64-bit `size`/`offset` are split into low/high halves — see
[Design notes](#design-notes) for why.

---

