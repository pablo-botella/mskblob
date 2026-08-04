---
mkskill:
  pos: 30
  in: readme
---

## Concepts

| Term | What it is |
|------|------------|
| **Blob** | One `.blob` file: a 64-byte header, a 64-byte GUID, an index of entries, and the concatenated bytes. Self-describing and immutable. |
| **Item** | One resource — the single unit of both a manifest and a blob's index. Declarative fields (`URL`, `Key`, `Filename`, `RestType`, `Src`) say *what* to pack; `Size`/`CRC32`/`Offset` are computed by the writer. |
| **Manifest** | The interchange format: an optional id plus items. Build a blob from one, or produce one from a blob. The JSON you edit by hand. |
| **GUID** | A random RFC-4122 v4 id stamped in the header. The sync token: a loader can demand the blob's id match what it was built against. |
| **Base** | The URL prefix a blob is mounted under. Entry URLs are stored **relative** to it; `Handler(base)` strips the base before lookup. |
| **Sealed index** | Lookups go through the in-memory index only. A path that isn't a packed entry is a 404 — there is no filesystem access and no traversal. |

A blob is **not updatable in place**. To change one: `dump` it to a directory
(`-manifest` to get the manifest too), edit, then `create` a fresh blob. It was
never meant to be a database — it's a build artifact.

---

