---
mkskill:
  pos: 80
---

## Serve blobs over HTTP

```
mskblob serve -config server.json   # the config names the blobs
mskblob serve -auto site.blob       # the blob carries its config
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

Each blob mounts as a sub-mux under its `base`; static entries stream lazily and **template** entries are rendered (`html/template`) with the merged variables. `vars` and `headers` exist at two levels — global and per-blob — and merge, the blob's winning. Serves HTTPS when `tls.cert`/`tls.key` are set, else plain HTTP. Only `blobs[].file` is required (`addr` defaults to `:8080`, `base` to `/`). An entry flagged `nomux` is never routed, whatever its url.

### Serving a blob nested in another

A nested blob is served only when the config names it — nothing is mounted by itself. `internal_path` lists the **keys to descend through**, outermost first, one nested blob per element; it is an array because a key may contain slashes of its own:

```json
{
  "blobs": [
    { "file": "site.blob", "base": "/" },
    { "file": "site.blob", "base": "/docs/",    "internal_path": ["/docs"] },
    { "file": "site.blob", "base": "/docs/es/", "internal_path": ["/docs", "/es"], "id": "947d…" }
  ]
}
```

The nested blob is read in place, straight from its section of the file, and a file listed by several entries is opened once. `id` checks the blob **finally mounted**. A key that does not exist, or is not a nested blob, stops the server from starting.

### Default document and extension-less URLs

A mount serves exact matches only: `/` and `/docs/` are a 404, because no entry has that url. Two options, **per mount** and off unless declared, say what else to try when the URL is not an entry — each a list tried in order, the first hit winning:

```json
{ "file": "site.blob", "base": "/",
  "remove_extensions": [".html"],
  "default_document": ["index.html"] }
```

| Option | Applies when the URL… | Tries | Example |
|---|---|---|---|
| `remove_extensions` | has no extension, or ends in `/` | the URL (minus its slash) + each extension | `/about` → `about.html` |
| `default_document` | is the root, or ends in `/` | the URL + each name | `/docs/` → `docs/index.html` |

An entry by the exact URL always wins and is served as it is — there are **no redirects**, so `/about.html` keeps working next to `/about`. When both apply the extensions go first. Nothing else is guessed: `/docs` without its slash is not the folder and stays a 404. A `nomux` entry is never found this way; a template found this way is rendered like any other.

### HTTPS with an encrypted key

`tls.cert` and `tls.key` are PEM files. The key may be **encrypted** — PKCS#8 with PBKDF2 and AES or triple-DES, what `openssl` writes (`BEGIN ENCRYPTED PRIVATE KEY`). Its password is never in the config, only where to find it:

```json
"tls": { "cert": "cert.pem", "key": "key.pem",
         "password_file": "/run/secrets/tls_password",
         "password_env": "MSKBLOB_TLS_PASSWORD" }
```

`password_file` is a file holding the password (a trailing newline is dropped) — a mounted Docker/Kubernetes secret fits; `password_env` names an environment variable. With both, the file wins, and the variable is used when the file is not there. An unencrypted key needs neither. A key derived with scrypt, a legacy `DEK-Info` PEM or a PFX is refused with a message saying so.

### Self-contained: the blob carries its own config

With `-config` the config is the entry point and names the blobs to open. `-auto` turns it round: the blob is the entry point and brings the config that serves it — one file to deploy.

```
mskblob serve -auto site.blob
```

The config is an entry of the blob at the fixed key **`/mskblob/auto/site.json`**, flagged `auto,nomux` and with no url, so it is never served itself:

```json
{ "key": "/mskblob/auto/site.json", "restype": "auto,nomux", "src": "site.json" }
```

Its content is the same JSON as above, except that `file` may be left out, meaning **this same blob** — the config cannot know what the file will be called once deployed. `internal_path` then descends from it, and a `file` with a value is still an external file:

```json
{
  "blobs": [
    { "base": "/", "default_document": ["index.html"] },
    { "base": "/docs/", "internal_path": ["/docs"] }
  ]
}
```

If the entry is missing, lacks `auto` or `nomux`, has a url, or is not valid JSON, the server does not start. The config of a nested blob is ignored when its parent is served. `-auto` and `-config` cannot be combined: to serve the blob another way, pass a config file and the one inside is not looked at.

The certificate can travel in the blob too. With `-auto`, `tls.cert` and `tls.key` are not paths but **keys of entries of the blob itself**, which must live under `/mskblob/` and be flagged `auto,nomux` — without those attributes they cannot be used:

```json
{ "key": "/mskblob/auto/cert.pem", "restype": "auto,nomux", "src": "cert.pem" },
{ "key": "/mskblob/auto/key.pem",  "restype": "auto,nomux", "src": "key.pem" }
```

```json
"tls": { "cert": "/mskblob/auto/cert.pem", "key": "/mskblob/auto/key.pem",
         "password_env": "MSKBLOB_TLS_PASSWORD" }
```

They are never served, but the blob now **contains the private key**, and `dump` extracts it like any entry: encrypt the key, so the file alone is not enough, and keep its password outside. To use a certificate on disk instead (say, one renewed without rebuilding the blob), serve with `-config`.

