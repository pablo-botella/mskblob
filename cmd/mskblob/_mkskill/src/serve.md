---
mkskill:
  pos: 80
---

## Serve blobs over HTTP

```
mskblob serve -config server.json
```

```json
{
  "addr": "0.0.0.0:8080",
  "tls": { "cert": "cert.pem", "key": "key.pem" },
  "vars": { "title": "My site" },
  "headers": [ { "name": "Cache-Control", "value": "public, max-age=3600" } ],
  "blobs": [
    { "file": "img.blob",  "base": "/img/", "id": "947d…" },
    { "file": "site.blob", "base": "/", "vars": { "section": "home" } }
  ]
}
```

Each blob mounts as a sub-mux under its `base`; static entries stream lazily and **template** entries are rendered (`html/template`) with the merged variables. `vars` and `headers` exist at two levels — global and per-blob — and merge, the blob's winning. Serves HTTPS when `tls.cert`/`tls.key` are set, else plain HTTP. All fields except `blobs[].file`/`base` are optional (`addr` defaults `:8080`, `base` defaults `/`).

