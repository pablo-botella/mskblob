---
mkskill:
  pos: 90
  in: readme
---

## Design notes

**Why a GUID, and why pinning matters.** Each `Write` stamps a fresh GUID unless
`Options.ID` is set. The GUID is a content-deploy sync token: `Load(path,
expectID)` rejects a blob whose GUID doesn't match, so you can't accidentally
serve last week's data file against this week's binary. Build pipelines that want a
stable id across rebuilds (e.g. a cache key) pin one with `Options.ID` / `create
-id`; everything else in a blob is reproducible, so a pinned id makes the whole
file reproducible. And because a pinned id *is* the content's identity, `Write` can
short-circuit: with `Options.SkipUnchanged` (or `create -skip-unchanged`) a target
already carrying that id is left untouched and no `Src` is read — `Load`/`ReadHeader`
*check* the id, `SkipUnchanged` *acts* on that check to spare a needless pack.

**Why hex strings, split low/high.** JSON numbers are float64 — large 64-bit sizes,
offsets and CRCs lose precision or render as unreadable 19-digit integers that
*mean nothing* to a human. So computed numbers are hex strings (`0x15BF411A`), and
64-bit values are split into 32-bit `low`/`high` halves, exactly like the binary
header. A CRC reads as a CRC; a size over 4 GiB is still exact. Since these fields
are ignored on input anyway, this is purely about making `dump`/`list` output
legible.

**Why not updatable.** A blob is a build artifact, not a store. Rewriting one entry
would mean shifting every following offset and recomputing the data CRC — i.e.
rewriting the file. So the supported edit loop is explicit: `dump` → edit →
`create`. This keeps the format dead simple and the reader trivially safe.

**Sealed index, no traversal.** The runtime never resolves a request against the
filesystem. `Handler` looks the (base-stripped) path up in the in-memory index; a
miss is a 404. There is no way to reach a path that wasn't packed.

---

