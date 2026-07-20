# ✨ starpulse

A single-binary smolweb CMS. One SQLite file, four doors in:

- **gemini://** (with **titan://** editing via client certificates)
- **http://** and **https://** (automatic Let's Encrypt)
- **ssh** — a full TUI gemini browser in your terminal (`ssh guest@host`),
  with a pico-style full-screen editor when you log in as `admin`
- **telnet** — the same TUI browser, read-only, over honest-to-goodness
  telnet (`telnet host`) — BBS nostalgia included
- **tor** hidden service (managed automatically — every enabled door gets an
  onion port: web, gemini, ssh, and telnet)

Content is [gemtext](https://geminiprotocol.net/docs/gemtext.gmi), authored in a
no-frills web admin, over titan, or through the built-in **REST API** and
**MCP server** (point Claude or any MCP client at `/mcp` and edit your site
conversationally). The public site ships **zero JavaScript**; the admin editor
uses a sprinkle for live preview, and degrades cleanly without it.

Pure Go, `CGO_ENABLED=0`, cross-compiles everywhere.

## Quick start

```sh
STARPULSE_ADMIN_PASSWORD=changeme STARPULSE_HTTP_ADDR=:8080 starpulse serve
```

Browse http://localhost:8080 — a starter site is seeded on first run.
Log in at `/login`, then use the ✎ link in any page footer.

### Install as a service (Linux)

```sh
sudo starpulse install --hostname example.org
```

```
starpulse serve          run the server
starpulse status         service health, per-protocol view graphs, top pages
starpulse doctor         config / TLS / DNS / tor connectivity checks
starpulse install        set up as a hardened systemd service (Linux, root)
starpulse uninstall      remove it (prompts before touching data)
starpulse self-update    update from the latest GitHub release
starpulse import <dir>   import a file-tree site into the database
starpulse hash-password  bcrypt-hash a password for config.yaml
```

Installs to `/opt/starpulse`, writes a sample config to
`/etc/starpulse/config.yaml` (with a generated admin password), stores data in
`/var/lib/starpulse`, and registers a hardened systemd unit that runs as a
dedicated non-root user (`CAP_NET_BIND_SERVICE` only). `sudo starpulse
uninstall` undoes it (prompting before touching your data);
`sudo starpulse self-update` pulls the latest GitHub release.

### Docker

```sh
docker run -d -p 80:80 -p 443:443 -p 1965:1965 \
  -e STARPULSE_HOSTNAME=example.org \
  -e STARPULSE_ADMIN_PASSWORD=changeme \
  -e STARPULSE_HTTPS=true \
  -v starpulse-data:/data \
  ghcr.io/jclement/starpulse:latest
```

## Configuration

`config.yaml` is looked for at `$STARPULSE_CONFIG`,
`~/.config/starpulse/config.yaml`, then `/etc/starpulse/config.yaml`.
Every key has a `STARPULSE_*` environment override.

```yaml
hostname: example.org
admin_password: "changeme"     # or a bcrypt hash from `starpulse hash-password`
data_dir: /var/lib/starpulse

gemini: { enabled: true, addr: ":1965" }
http:   { enabled: true, addr: ":80" }
https:  { enabled: true, addr: ":443", acme: true, acme_email: "you@example.org" }

titan:
  enabled: true
  cert_fingerprints: ["<sha256 of your client cert>"]

tor:
  enabled: true                # runs a private tor; every enabled door is
                               # forwarded (onion:80/1965/22/23)
  # onion: xyz.onion           # or point at an externally-managed hidden service

timezone: "America/Edmonton"  # IANA zone for displayed timestamps (empty = server local)
max_upload_bytes: 10485760
keep_versions: 25
```

## How content works

Everything lives in `starpulse.sqlite` — pages, uploaded files, versions, and
stats. Pages are gemtext at paths like `/index.gmi` and
`/posts/2026-07-19-hello.gmi` (served extensionless: `/posts/2026-07-19-hello`).
Dated filenames get listed newest-first and feed `/feed.xml`.

**Special files**, inherited down the folder tree:

| file | what |
|---|---|
| `.header` / `.footer` | gemtext included above/below every page in that folder and below |
| `.theme` | CSS applied to the web rendering of that folder and below |

**Directives** inside any page:

| directive | renders |
|---|---|
| `{{list [folder] [limit]}}` | link list of a folder's pages (dated first, newest first) |
| `{{include /path}}` | another page's content, inline |
| `{{now [limit]}}` | your latest "now" micro-posts |
| `{{latest_now}}` / `{{latest_now_date}}` | just the newest now-post's text / date (inline) |
| `{{random /path}}` | one random line from a file (taglines!) |
| `{{count}}` | the page's view counter |
| `{{rev}}` | the page's revision number (edits so far) |
| `{{updated}}` | the page's last-edit date |
| `{{version}}` | server build version |

**Now posts** are lightweight timestamped updates — post from the admin, SSH,
API, or MCP; they render anywhere you put `{{now}}` (the starter site seeds a
`/now.gmi` doing exactly that).

Every save keeps the previous content as a **version** (deletes too), so
undo is always one click away. Per-page **stats** are broken down by door:
`http`, `gemini`, `http+tor`, `gemini+tor`. Full-text **search** (SQLite FTS5)
is at `/search` on both protocols.

## Editing

- **Web**: log in at `/login` → subtle ✎ edit link on every page, plus
  `/admin` for uploads, history, stats, and now-posts.
- **Titan**: enable `titan` and allowlist your client cert's SHA-256
  fingerprint; then edit straight from Lagrange (fetch raw source at
  `gemini://host/raw/<path>` with the same cert). Zero-byte upload = delete.
- **REST**: `Authorization: Bearer <admin_password>`; see `/api/pages`,
  `/api/search`, `/api/now`, `/api/versions`, `/api/stats`.
- **SSH**: `ssh admin@host` (admin password — or list `authorized_keys` under
  `ssh:` in the config for key-only auth, which disables admin passwords over
  SSH entirely) drops you into the TUI
  browser with editing — `e` edits the page you're reading, `c` creates a page,
  `n` posts a now update, `x` deletes, `ctrl+s` saves. `ssh guest@host -p 2222`
  gives read-only browsing (tab/enter to follow links, `/` to search).
- **MCP**: streamable-HTTP server at `/mcp` (same bearer token) with tools for
  reading, writing, searching, stats, now-posts, and version restore:

```sh
claude mcp add --transport http mysite https://example.org/mcp \
  --header "Authorization: Bearer <admin_password>"
```

## Migrating from a file tree

```sh
starpulse import ./content
```

Imports a directory (owg-capsule conventions understood: `_header.gmi` →
`.header`, `{{index}}` → `{{list}}`, `{{counter}}` → `{{count}}`, …).

## Development

```sh
go test ./...
STARPULSE_HTTP_ADDR=:8080 STARPULSE_ADMIN_PASSWORD=dev go run . serve
```

Releases are built by GitHub Actions: a docker image on
`ghcr.io/jclement/starpulse` for every push to main, and binary tarballs for
linux/darwin/windows/freebsd on every `v*` tag.
