---
mkskill:
  pos: 240
  in: ai*
---

## Rules & gotchas

- **Identity is url and/or key.** An entry needs at least one of `url`/`key`
  (static→url, template→key; both is allowed but unusual). `Write` rejects an item
  with neither, and requires unique non-empty urls and unique non-empty keys. `Open`
  builds two indexes: `byURL` (served over HTTP) and `byKey` (logical lookup).
- **Templates aren't served by the default handler.** A template/parse entry needs
  rendering (data + funcs = host logic); the package can't do it. The default
  handler serves only static — a template (or any non-static, or an absent URL) 404s.
  Render it from a `Middleware` (return `DispatchDone`) or fetch it with `GetByKey`
  and render wherever you like.
- **`nomux` means not routed.** `Handler` takes an entry flagged `nomux` for absent:
  it is not served and the middleware is handed `nil`, whatever its url. `GetByKey`,
  `Bytes` and `Reader` are unaffected — a `nomux` entry is an asset reached by key.
- **mskblob's own flags live in the second byte and demand `nomux`.** The low byte of
  `RestType` (`0x00FF`) mirrors miniskin's item types; the second (`0xFF00`) is
  mskblob's: `Mskblob 0x0100`, `MskBlobAuto 0x0200`. An entry carrying any of them is
  never served, so `Write` requires a **key, no url and `nomux`** — and a blob holding
  one without `nomux` (packed by something else) **does not open**: refused, not read
  as a plain entry.
- **A nested blob is mounted, never served.** An entry marked `mskblob,nomux` holds another
  blob, and `Write` demands it carry a **key and no url** — so it never enters the
  routing index and no handler, present or future, can hand out the container's bytes
  as an asset. `OpenBlob(key)` / `LoadBlob(key, id)` mount it in place over its section
  of the parent: an ordinary `*Blob`, nestable again. The tree shares the root's
  descriptor, so a child's `Close` is a no-op and closing the root turns every later
  read into `ErrClosed`. Nothing in the binary format changes — a container is a normal
  blob whose entry happens to hold one.
- **A blob cannot include itself.** `Write` rejects any item whose `src` is the output
  file, nested or not, before creating anything, so the existing file is left
  untouched. They are compared as files, not as path strings: a relative path or a
  link to the same file is caught too. A blob nested in a *different* output is a
  plain byte copy made at write time, so no cycle can exist in the format.
- **Streaming, not loading.** Static serving uses `io.SectionReader` straight from
  the file — nothing resident in RAM, which is the whole point for a 245 MB asset set.
  Prefer `Reader` over `Bytes` for large entries.
- **Case sensitivity is a blob property.** By default lookups are **case-sensitive**
  (exact match, like miniskin's embedded routes). Build with `nocase` (Options /
  manifest / `manifest`/`create -nocase`) and the blob records a header flag; then
  `GetByURL`/`GetByKey` (and the served Handler) **fold case**. This matters on
  case-sensitive filesystems (Linux), where `Logo.JPG` and `logo.jpg` are distinct
  files — under `nocase` such a pair collides and `Write` rejects it. The stored
  URL/Key keep their original case; only matching folds.
- **GUID is the deploy sync token.** Each `Write` stamps a fresh GUID unless
  `Options.ID` is set. `Load(path, expectID)` rejects a blob whose GUID doesn't
  match, so you can't serve a stale data file against a newer binary. Pin an id (via
  `Options.ID` / `create -id`) for a reproducible, cacheable file. Because a pinned
  id *is* the content's identity, `Options.SkipUnchanged` / `create -skip-unchanged`
  can skip the pack entirely (no `Src` read) when the target already carries that id
  — to force a rebuild, delete the `.blob` or change the id.
- **Hex strings, split low/high.** JSON numbers are float64; large 64-bit sizes /
  offsets / CRCs lose precision or read as meaningless 19-digit integers. So computed
  numbers are hex strings and 64-bit values are split into 32-bit low/high halves
  (like the binary header). They're ignored on input anyway — purely for legible
  `dump`/`list` output.
- **Not updatable in place.** A blob is a build artifact, not a store; rewriting one
  entry would shift every following offset and the data CRC. The edit loop is
  explicit: `dump` → edit → `create`.
- **Sealed index, no traversal.** The runtime never resolves a request against the
  filesystem. A miss is a 404; only packed entries are reachable.
- **Encapsulation.** Build via the manifest helpers, serve via `Handler`/middleware,
  access via `GetBy*`/`Reader`. Internal layout (binary format, indexes, offsets,
  alignment) is private — these signatures are stable across format changes.
