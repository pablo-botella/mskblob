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
