# ✨ starpulse

A single-binary smolweb CMS. One SQLite file, five doors in:

| door | what you get |
|---|---|
| **https / http** | the site as HTML, with automatic Let's Encrypt certificates |
| **gemini** | the same pages as gemtext, plus **titan** editing from a gemini client |
| **ssh** | a full TUI browser in your terminal — and a pico-style editor when you log in as `admin` |
| **telnet** | the same TUI browser, read-only. BBS nostalgia included |
| **tor** | a hidden service mirroring *every* enabled door |

Content is [gemtext](https://geminiprotocol.net/docs/gemtext.gmi), written in a
no-frills web admin, over titan, in the SSH editor, or through the built-in
**REST API** and **MCP server** — point Claude at `/mcp` and edit your capsule
by asking. The public site ships **zero JavaScript**, syntax highlighting
included; the admin uses a little, and degrades without it.

Pure Go, `CGO_ENABLED=0`, no external database, cross-compiles everywhere.

---

## Install

### Linux, as a service

```sh
curl -L https://github.com/jclement/starpulse/releases/latest/download/starpulse_linux_amd64.tar.gz | tar xz
sudo ./starpulse install --hostname example.org
```

Installs the binary to `/opt/starpulse`, writes `/etc/starpulse/config.yaml`
with a generated admin password (printed once), puts data in
`/var/lib/starpulse`, and registers a hardened systemd unit running as a
dedicated non-root user with `CAP_NET_BIND_SERVICE` only — so it binds :80,
:443 and :1965 without ever being root.

```sh
systemctl status starpulse     # is it up
journalctl -u starpulse -f     # what is it doing
sudo starpulse self-update     # pull the latest release
sudo starpulse uninstall       # undo (prompts before touching your data)
```

### Docker

```sh
docker run -d --name starpulse \
  -p 80:80 -p 443:443 -p 1965:1965 \
  -e STARPULSE_HOSTNAME=example.org \
  -e STARPULSE_ADMIN_PASSWORD=changeme \
  -e STARPULSE_HTTPS=true \
  -v starpulse-data:/data \
  ghcr.io/jclement/starpulse:latest
```

### Try it locally

```sh
STARPULSE_ADMIN_PASSWORD=dev STARPULSE_HTTP_ADDR=:8080 starpulse serve
```

Browse <http://localhost:8080> — a starter site is seeded on first run. Log in
at `/login`, then use the ✎ link in any page footer. **`/admin/manual` is a
built-in manual describing your own running site**, listing only the doors you
actually have switched on.

---

## Commands

```
starpulse serve          run the server (default)
starpulse status         health, per-door view graphs, top pages
starpulse doctor         config / TLS / DNS / tor connectivity checks
starpulse install        set up as a systemd service (Linux, root)
starpulse uninstall      remove it
starpulse self-update    update from the latest GitHub release
starpulse import <dir>   import a directory of files into the database
starpulse hash-password  bcrypt-hash a password for config.yaml
starpulse version
```

---

## Features

- **One binary, one file.** Pages, uploads, version history, view counts and
  the search index all live in `starpulse.sqlite`.
- **Versioned everything.** Every save keeps the previous content — deletes and
  renames too — so nothing is unrecoverable. A rename carries a page's history
  and view counts with it.
- **Full-text search** (SQLite FTS5) on the web, over gemini, and in the TUI.
- **Per-door statistics**: which pages, and whether they were read over http,
  gemini, ssh, telnet or tor.
- **Notes**: short entries — ordinary pages in a stream folder, so they get
  history, search and feeds like everything else, without cluttering listings.
- **Syntax highlighting** for code blocks, rendered server-side.
- **Automatic HTTPS**, and a **self-managed tor hidden service** forwarding
  every door you enable.
- **Five ways to edit**: web, titan, SSH, REST, MCP.

---

## Configuration

`config.yaml` is looked for at `$STARPULSE_CONFIG`,
`~/.config/starpulse/config.yaml`, then `/etc/starpulse/config.yaml`. Every key
has a `STARPULSE_*` environment override.

```yaml
hostname: example.org
admin_password: "changeme"      # or a bcrypt hash from `starpulse hash-password`
data_dir: /var/lib/starpulse
timezone: "America/Edmonton"    # IANA zone for displayed timestamps

gemini: { enabled: true, addr: ":1965" }
http:   { enabled: true, addr: ":80" }
https:  { enabled: true, addr: ":443", acme: true, acme_email: "you@example.org" }

ssh:
  enabled: true
  addr: ":22"
  authorized_keys:              # when set, admin password auth over SSH is
    - "ssh-ed25519 AAAA... you" # disabled entirely — keys only
telnet:
  enabled: true
  addr: ":23"

titan:
  enabled: true
  cert_fingerprints: ["<sha256 of your client cert>"]

tor:
  enabled: true                 # runs its own tor, forwards every enabled door
  # onion: xyz.onion            # or point at an externally managed service

highlight:
  enabled: true
  style: github
  dark_style: github-dark

feeds:
  author: "Your Name"
  limit: 30
  now:                          # publish now-posts as a feed
    enabled: false
    path: /now/feed.xml
    page: /now
  list: []                      # any other feed, e.g. a site-wide one

max_upload_bytes: 10485760
keep_versions: 25
```

---

## How content works

Pages are gemtext at paths like `/index.gmi` and `/posts/hello.gmi`, served
extensionless (`/posts/hello`). A path with no extension gets `.gmi`
automatically, so you cannot accidentally create an unviewable file.

**Special files**, inherited down the folder tree:

| file | what it does |
|---|---|
| `.header` / `.footer` | gemtext wrapped above/below every page in that folder and below |
| `.css` | CSS applied to that folder and below (created prefilled with the site's own colour variables) |
| `.feed` | marks the folder as publishing a feed, and configures it |

**Directives**, expanded when a page is served:

| directive | renders |
|---|---|
| `{{list [folder] [limit]}}` | link list of a folder's pages |
| `{{include /path}}` | another page's content, inline |
| `{{stream [folder] [limit]}}` | a folder's entries in full, newest first |
| `{{latest [folder] [body\|link\|title\|date]}}` | one part of a folder's newest entry |
| `{{now [limit]}}` / `{{latest_now}}` | the same, for the configured notes folder |
| `{{random /path}}` | one random line from a file |
| `{{count}}` | this page's view counter |
| `{{rev}}` | this page's revision number |
| `{{updated}}` | this page's last-edit date |
| `{{version}}` | server build version |

---

## Posts and feeds

**Feeds are opt-in.** A folder publishes one when you turn it on — the
*enable feed* link beside it in the admin page list. Nothing starts publishing
by itself. Turning it on writes a `.feed` file holding that feed's settings,
editable like any other page:

```
# Feed settings for this folder. Delete this file to stop publishing.
title: Field Notes
subtitle:
author: Jeff Clement
limit: 30
```

Inside a feed folder **every page is a post**, so filenames can stay plain. A
post's date is resolved in order:

1. a `YYYY-MM-DD-` prefix on the filename — authoritative, portable, visible;
2. a `date:` in front matter;
3. the day the page was created, from the database.

Outside a feed folder only the first two count, so ordinary pages stay undated
and list alphabetically. Feeds are served over HTTP *and* gemini, and each is
advertised in the HTML `<head>`.

Add `hide_files: true` to a `.feed` and the folder becomes a **stream**: its
pages are short notes rather than documents, so they stay out of `{{list}}`
and collapse in the admin. That is all a "now" page is — a gemlog without the
file details. `{{now}}` and the *+ note* button write into the folder named by
`now_folder`, and posting one over titan is just uploading to the folder
itself:

```sh
# in Lagrange: navigate to gemini://example.org/now/ and upload
titan://example.org/now/;mime=text/plain;size=N
```

On gemini the idiomatic feed is just a page of dated link lines — which
`{{list}}` already emits, so a folder index is subscribable in Lagrange or
Amfora with nothing enabled at all.

---

## Syntax highlighting

Fenced code blocks are highlighted **server side**, so the public site needs no
JavaScript. The language comes from the fence's alt text — 297 lexers via
[chroma](https://github.com/alecthomas/chroma). Blocks with no alt text, and
decorative ones (`banner`, `table`), are left alone.

The admin editor highlights as you type too: gemtext, CSS in a `.theme`, and
the `key: value` special files — hand-rolled, no editor library.

---

## Editing from elsewhere

- **Titan**: enable `titan` and allowlist your client certificate's SHA-256
  fingerprint (what Lagrange shows for an identity). With that identity active
  you are served each page's **raw source**, so an edit round-trips exactly
  instead of baking in inherited headers and expanded directives. A zero-byte
  upload deletes.
- **SSH**: `ssh admin@host` → `e` edit, `c` new page, `n` now-post, `x` delete,
  `ctrl+s` save, `ctrl+g` syntax help, `g` fuzzy page jump, `/` search.
- **REST**: `Authorization: Bearer <admin_password>` against `/api/pages`,
  `/api/now`, `/api/search`, `/api/versions`, `/api/stats`.
- **MCP**: streamable HTTP at `/mcp`.

```sh
claude mcp add --transport http mysite https://example.org/mcp \
  --header "Authorization: Bearer <admin_password>"
```

For OAuth clients (Claude Desktop's custom connectors) starpulse is its own
authorization server — discovery, PKCE authorization-code, client credentials
and refresh:

| field | value |
|---|---|
| server URL | `https://example.org/mcp` |
| client ID | `mcp` |
| client secret | your `admin_password` |

---

## Migrating from a directory of files

```sh
starpulse import ./content
```

Understands owg-capsule conventions: `_header.gmi` → `.header`, `{{index}}` →
`{{list}}`, `{{counter}}` → `{{count}}`.

---

## Development

```sh
go test ./...
STARPULSE_HTTP_ADDR=:8080 STARPULSE_ADMIN_PASSWORD=dev go run . serve
```

GitHub Actions builds a `ghcr.io/jclement/starpulse` image on every push to
main, and binary tarballs for linux/darwin/windows/freebsd on every `v*` tag.

## License

MIT
