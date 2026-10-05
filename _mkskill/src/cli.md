---
mkskill:
  pos: 60
  in: readme
---

## CLI

```
mskblob info     -blob <file>                  [-short]
mskblob list     -blob <file>                  [-md | -json] [-o f]
mskblob manifest -dir <dir>                    [-o f] [-base d] [-include g] [-exclude g] [-recurse] [-nocase]
mskblob create   -manifest <f> -out <blob>     [-id guid] [-base d] [-nocase] [-skip-unchanged]
mskblob dump     -blob <file> -baseout <dir>   [-manifest f]
mskblob dump     -blob <file> -file <key>      (-baseout d | -out f | -stdout)
mskblob serve    -config <file> | -auto <blob>
mskblob generate-claude-skill                  [-dst f] [-global] [-force]
mskblob generate-agent-docs                    [-dst f] [-force]
```

> **Every option is a named flag — order never matters.** If you forget one, add it
> at the end. There are no positional arguments, so nothing is silently swallowed: an
> unknown flag or a stray argument is a clear error, and a command run with no flags
> prints its own help.

> `SKILL.md` and `AGENTS.md` are **generated** from the `ai/` sources (a template
> plus `ai/core/*.md`), the same model miniskin uses — edit the sources, not the
> generated files, then re-run the two `generate-*` commands. `generate-claude-skill`
> writes the project-local skill by default; `-global` installs it into
> `~/.claude/skills/mskblob/SKILL.md` (available from every project, no elevation —
> it's under your own home), mutually exclusive with `-dst`.

### `info` — header at a glance

```
$ mskblob info -blob img.blob
blob:    img.blob
version: 1
id:      947d88a9-7196-490c-87dd-9a8a1262b0ec
entries: 2
nocase:  false
dataCRC: 0x6d38580c
data:    18 bytes

$ mskblob info -blob img.blob -short
947d88a9-7196-490c-87dd-9a8a1262b0ec  2 entries  18 bytes  nocase=false
```

### `list` — the contents

Prints a GitHub-style markdown document (a header section plus a padded items
table) by default.

```
$ mskblob list -blob img.blob
## blob

- id: `947d88a9-7196-490c-87dd-9a8a1262b0ec`
- version: 1
- entries: 2
- nocase: false
- dataCRC: 0x6d38580c
- data: 18 bytes

## items

| URL          | SIZE | CRC32      | TYPE   | FILENAME     |
|--------------|-----:|------------|--------|--------------|
| app.css      |   15 | 0x034CA4BD | static | app.css      |
| img/logo.png |    3 | 0x15BF411A | static | img/logo.png |
```

Output flags:

- `-json` — emit the manifest JSON instead of the markdown table.
- `-md` — emit the markdown table (the default; explicit for clarity). Mutually
  exclusive with `-json`.
- `-o <file>` — write to that file instead of stdout (same meaning as `manifest -o`).
  Without `-o` the output goes to stdout, so you can view it or redirect it with `>`.

### `manifest` — scan a directory into an editable manifest

`create` builds *from a manifest*, so the usual flow is: generate one from a
directory, **review and tweak the flags by hand**, then build.

```sh
# scan ./assets/prod-img for images, src relative to ./assets, write a manifest:
mskblob manifest -dir ./assets/prod-img -base ./assets -include "*.jpg,*.png" -recurse -o prod-img.json
```

- does **not** recurse unless `-recurse`;
- `-include` / `-exclude` are comma-separated globs matched on the file name;
- `-nocase` makes the matching case-insensitive (`*.jpg` matches `.JPG`) **and**
  marks the resulting blob case-insensitive — recorded in its header, so runtime
  lookups fold case (important on Linux, where `Logo.JPG` ≠ `logo.jpg`);
- `-base` makes `src` relative to a chosen root so the manifest is portable
  (default: the scanned dir);
- `-o` writes to a file (default: stdout).

Every item comes out as `restype: "static"` — edit them before building.

### `create` — build a blob

```sh
mskblob create -manifest prod-img.json -out prod-img.blob -base ./assets
```

- the manifest may be the full JSON object `{id?, items:[...]}`, a bare `[ … ]`
  items array, or a plain text list (one `"<url>\t<src>"` — or just `"<src>"` —
  per line; `#` comments and blank lines ignored);
- relative `src` paths resolve against `-base` (default: the manifest's own
  directory);
- the GUID is taken from `-id`, else the manifest's `id`, else freshly generated;
- missing `filename` defaults to `url`; missing/zero `restype` defaults to `static`;
- `-skip-unchanged` makes `create` a no-op when the output blob already exists with
  the **pinned** id — a pinned id is the content's identity, so no source file is
  read (just the 128-byte header) and the file is left untouched. It needs a pinned
  id; with an auto id every build mints a fresh guid, so there's nothing to compare
  and the build runs. Useful when a project rebuilds many blobs repeatedly; to force
  a rebuild, delete the `.blob` or change the id.

A **nested blob** takes no special flag — it is an item like any other, with a `key`,
no `url` and a built `.blob` as its source. Levels are built bottom up, one `create`
per level:

```sh
mskblob create -manifest img.json  -out img.blob    # the inner blob
# site.json: [{"key": "/img", "filename": "img.blob", "restype": "mskblob,nomux", "src": "img.blob"}]
mskblob create -manifest site.json -out site.blob   # the container
```

### `dump` — extract a blob's files

```sh
mskblob dump -blob img.blob -baseout ./out                            # ./out/<files>
mskblob dump -blob img.blob -baseout ./out -manifest ./out/manifest.json  # + the manifest
mskblob create -manifest ./out/manifest.json -out img2.blob          # round-trip
```

`dump` is for **extracting the asset files**: it writes every entry under `-baseout`
(its base directory), each at its url — or at its **key** when it has no url, as a
template or a nested blob does. It does **not** write a manifest by default —
`-manifest <file>` is an extra that also emits the manifest in one command, with each
`src` pointing at the just-written files, so `create -manifest <file>` recreates the
blob. Entry paths are validated — an entry that would escape the base dir (`..`,
absolute) or has no name at all is rejected.

**One entry at a time.** `dump -file <key>` extracts just the entry with that **key**
to exactly one destination:

```sh
mskblob dump -blob img.blob -file /patata/frita.png -baseout ./out  # ./out/patata/frita.png
mskblob dump -blob img.blob -file /patata/frita.png -out frita.png  # ./frita.png (cwd-relative)
mskblob dump -blob img.blob -file /patata/frita.png -out ./imgs/    # ./imgs/frita.png (dir → basename)
mskblob dump -blob img.blob -file /patata/frita.png -stdout         # bytes to stdout
```

`-baseout` keeps the entry at its subpath; `-out` is an explicit path (a directory if
it ends in a separator, then the basename is appended); `-stdout` writes the raw bytes.
`-out`/`-stdout` apply only with `-file`, and a missing or duplicate destination is an
error.

### `serve` — run a web server from a JSON config

The standalone server, driven by one JSON file. It mounts one or more blobs, each
under its own base; static entries stream lazily and **template** entries are
rendered with the configured variables. Variables and extra headers exist at two
levels — **global** and **per-blob** — and merge, with the blob's winning. Serves
HTTPS when `tls.cert`/`tls.key` are set (or plain HTTP behind a Cloudflare-style
tunnel).

```sh
mskblob serve -config server.json
```

```json
{
  "addr": "0.0.0.0:8080",
  "tls": { "cert": "cert.pem", "key": "key.pem" },
  "vars": { "title": "My site", "env": "prod" },
  "headers": [ { "name": "Cache-Control", "value": "public, max-age=3600" } ],
  "blobs": [
    { "file": "img.blob",  "base": "/img/",  "id": "947d…" },
    { "file": "site.blob", "base": "/", "vars": { "section": "home" },
      "headers": [ { "name": "X-Frame-Options", "value": "DENY" } ] }
  ]
}
```

`tls`, `vars`, `headers`, and per-blob `id`/`vars`/`headers` are all optional;
`addr` defaults to `:8080` and `base` to `/`. This is also the reference for how
`Blob.Handler` is wired with a middleware — your own app does the same shape.

**Serving a blob nested in another.** A [nested blob](#nested-blobs) is never served
through its parent: it has no url, and `serve` mounts nothing by itself. Name it —
`internal_path` lists the **keys to descend through**, outermost first, one nested
blob per element:

```json
{
  "blobs": [
    { "file": "site.blob", "base": "/" },
    { "file": "site.blob", "base": "/docs/",    "internal_path": ["/docs"] },
    { "file": "site.blob", "base": "/docs/es/", "internal_path": ["/docs", "/es"], "id": "947d…" }
  ]
}
```

It is an array rather than one slash-separated string because a key may contain
slashes of its own. Absent or empty, the entry mounts the file itself, as before. The
mounted blob is read in place, straight from its section of the file — nothing is
extracted — and a file listed by several entries is opened once. `id` checks the
blob **finally mounted**, not the file that contains it. A key that does not exist,
or is not a nested blob, stops the server from starting.

**What a URL that is not an entry may still find.** By default a mount serves exact
matches only: `/` and `/docs/` are a 404, because no entry has that url. Two options,
**per mount** and off unless declared, say what else to try — the lists are tried in
order and the first hit wins:

```json
{ "file": "site.blob", "base": "/",
  "remove_extensions": [".html"],
  "default_document": ["index.html"] }
```

| Option | Applies when the URL… | Tries | Example |
|---|---|---|---|
| `remove_extensions` | has no extension, or ends in `/` | the URL (minus its slash) + each extension | `/about` → `about.html` |
| `default_document` | is the root, or ends in `/` | the URL + each name | `/docs/` → `docs/index.html` |

An entry by the exact URL always wins, and is served as it is — there are **no
redirects**: `/about.html` keeps working next to `/about`. When both options apply
the extensions go first (`/docs/` finds `docs.html` before `docs/index.html`). Nothing
else is guessed: `/docs` without its slash is not the folder and stays a 404 — what
is not declared does not exist. A `nomux` entry is never found this way, and a
template found this way is rendered like any other.

**HTTPS and an encrypted key.** `tls.cert` and `tls.key` are PEM files. The key may
be **encrypted** — PKCS#8 with PBKDF2 and AES or triple-DES, what `openssl` writes by default
(`BEGIN ENCRYPTED PRIVATE KEY`). Its password is never in the config, only where to
find it:

```json
"tls": { "cert": "cert.pem", "key": "key.pem",
         "password_file": "/run/secrets/tls_password",
         "password_env": "MSKBLOB_TLS_PASSWORD" }
```

`password_file` is a file holding the password (a trailing newline is dropped) — a
mounted Docker/Kubernetes secret fits; `password_env` names an environment variable.
With both, the file wins, and the variable is used when the file is not there. An
unencrypted key needs neither. A key derived with scrypt, a legacy `DEK-Info` PEM or
a PFX is refused with a message saying so.

**Self-contained: the blob carries its own config.** With `-config` the config is the
entry point and names the blobs to open. `-auto` turns it round — the blob is the
entry point and brings the config that serves it, like a disk with its `autoexec.bat`:

```sh
mskblob serve -auto site.blob
```

The config is an ordinary entry of the blob at the fixed key
**`/mskblob/auto/site.json`**, flagged `auto,nomux` and with no url, so it is never
served itself:

```json
{ "key": "/mskblob/auto/site.json", "restype": "auto,nomux", "src": "site.json" }
```

Its content is the same JSON as above, with one difference: `file` may be left out,
meaning **this same blob** — the config cannot know what the file will be called once
deployed. `internal_path` then descends from it, and a `file` with a value is still an
external file:

```json
{
  "blobs": [
    { "base": "/" },
    { "base": "/docs/", "internal_path": ["/docs"] }
  ]
}
```

If the entry is missing, lacks `auto` or `nomux`, has a url, or is not valid JSON, the
server does not start. `-auto` and `-config` cannot be combined — nothing is merged:
to serve the blob another way, pass a config file and the one inside is not looked at.
The config of a nested blob is ignored when its parent is served.

With `-auto` the certificate travels in the blob too. `tls.cert` and `tls.key` are
then not paths but **keys of entries of the blob itself**, which must live under
`/mskblob/` and be flagged `auto,nomux` — without those attributes they cannot be
used, and the server does not start:

```json
{ "key": "/mskblob/auto/cert.pem", "restype": "auto,nomux", "src": "cert.pem" },
{ "key": "/mskblob/auto/key.pem",  "restype": "auto,nomux", "src": "key.pem" }
```

```json
"tls": { "cert": "/mskblob/auto/cert.pem", "key": "/mskblob/auto/key.pem",
         "password_env": "MSKBLOB_TLS_PASSWORD" }
```

Being `nomux` and url-less they are never served. But the blob now **contains the
private key**, and `dump` extracts it like any entry: encrypt the key, so that the
file alone is not enough, and keep its password outside — in the environment or in a
file. To use a certificate on disk instead (say, one renewed without rebuilding the
blob), serve with `-config`.

---

