<div align="center">

# artifact

*Turn a local file into a URL a human can open.*

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev)
[![SQLite](https://img.shields.io/badge/Metadata-SQLite-3f7f5f?style=flat)](https://sqlite.org/)
[![Container](https://img.shields.io/badge/Image-distroless%2Fstatic-2496ED?style=flat&logo=docker&logoColor=white)](https://github.com/GoogleContainerTools/distroless)
[![Platform](https://img.shields.io/badge/Platform-macOS%20%7C%20Linux-lightgrey)]()

</div>

---

```console
$ artifact report.html
https://artifact.example.com/aB3xK9qZ
```

Markdown and HTML render as pages. Images, video, and PDFs preview inline. Logs
display as text. Everything else downloads. The server is one static binary over
one directory.

## Features

- **One command** — prints the URL and nothing else, so `URL=$(artifact shot.png)` works.
- **Renders on the way out** — Markdown becomes a styled page; HTML is served byte-for-byte.
- **Stable URL, frozen versions** — republish as often as you like; `@1`, `@2` keep their exact bytes forever.
- **Video that seeks** — range requests handled by `http.ServeContent`.
- **Nothing to operate** — files on disk, metadata in SQLite. No object storage, no database server.

## Install

```bash
go install github.com/10/artifact@latest    # CLI
go build -o artifactd ./cmd/artifactd       # server, from a checkout
```

## CLI

```bash
artifact report.html                        # upload, print URL
make 2>&1 | artifact --filename build.log - # standard input
artifact up --slug daily-report report.md   # stable URL
artifact update daily-report report.md      # new version, same URL
artifact list --limit 20
artifact info aB3xK9qZ
artifact rm aB3xK9qZ
artifact open aB3xK9qZ
```

Stdout is the URL. Progress, warnings, and errors go to stderr:

```bash
URL=$(artifact screenshot.png) || exit 1
```

`--json` prints the full API response instead. Exit codes: `0` ok, `1` runtime
error, `2` bad usage or no config, `3` bad token, `4` not found.

Config resolves flags, then `ARTIFACT_ENDPOINT` / `ARTIFACT_TOKEN`, then
`~/.config/artifact/config.toml`:

```toml
endpoint = "https://artifact.example.com"
token    = "…"
```

Give that file mode `0600` or the CLI warns on every run.

## URLs

```
/aB3xK9qZ              latest, rendered
/aB3xK9qZ/raw          latest, never rendered
/aB3xK9qZ@2            version 2, frozen forever
/aB3xK9qZ/report.html  same file, path ends in .html
```

IDs are 8 random base62 characters, never reused — deleting an artifact retires
its ID, so an old link 404s instead of resolving to someone else's file.

`--slug` publishes at a name instead: lowercase and hyphens, 3–64 characters,
never exactly 8 so slugs can't collide with IDs. `api`, `healthz`, `raw`,
`static`, `favicon.ico`, and `robots.txt` are reserved.

## Rendering

The extension decides. Unknown ones get sniffed, then fall back to a download.

| Extension | Result |
| --- | --- |
| `.md` `.markdown` | Rendered page — GFM tables, task lists, code blocks |
| `.html` `.htm` | Served byte-for-byte |
| `.png` `.jpg` `.jpeg` `.gif` `.webp` `.avif` `.svg` | Inline image |
| `.mp4` `.webm` `.mov` `.mp3` `.wav` `.m4a` `.caf` | Inline, seekable |
| `.pdf` | Inline |
| `.txt` `.log` `.json` `.yaml` `.csv` `.xml` `.go` `.py` `.ts` `.js` `.sh` `.sql` | Plain text |
| anything else | Download |

Markdown renders per request, so an update shows immediately and a template fix
reaches every existing artifact. The template is embedded: dark mode, no
JavaScript, no external requests. Raw HTML inside Markdown passes through, since
`.html` files are served verbatim anyway.

## API

`/api/*` needs `Authorization: Bearer $ARTIFACT_TOKEN`. Artifact URLs are public.

```bash
curl -sS -X POST \
  -H "Authorization: Bearer $ARTIFACT_TOKEN" \
  --data-binary @report.html \
  "$ARTIFACT_ENDPOINT/api/upload?filename=report.html" | jq -r '.url'
```

`POST /api/upload`, `PUT` and `DELETE /api/artifacts/{id}`,
`GET /api/artifacts`, `GET /api/artifacts/{id}`, and an open `GET /healthz`.
Errors carry a stable `error.code`. Full shapes: [docs/api.md](docs/api.md).

## Server

```bash
export ARTIFACT_TOKEN="$(openssl rand -hex 32)"
export ARTIFACT_BASE_URL="https://artifact.example.com"
artifactd
```

Both are required and validated at startup. Optional: `ARTIFACT_ADDR`
(`:8080`, or `:$PORT` when `PORT` is set), `ARTIFACT_DATA_DIR` (`/data`),
`ARTIFACT_DB`, `ARTIFACT_MAX_UPLOAD_SIZE` (100 MiB), `ARTIFACT_LOG_LEVEL`
(`info`).

Logs are JSON on stdout, one line per request, never the token or a body.
`SIGTERM` drains in-flight requests, then closes the database.

## Deploy

```bash
docker build -t artifact .
docker run -d -p 8080:8080 -v artifact-data:/data \
  -e ARTIFACT_TOKEN=… -e ARTIFACT_BASE_URL=… artifact
```

distroless/static, running as uid 65532. A named volume works as-is; a bind
mount needs `chown -R 65532:65532` first. Coolify, systemd, health checks, and
backups: [SETUP.md](SETUP.md).

## Security

- Bearer token only, compared in constant time. No cookie is ever set.
- **Give it its own hostname.** Uploaded HTML and SVG run scripts on that origin, so host nothing else there.
- Filenames are sanitized and kept in SQLite. They never touch a filesystem path.
- The token can overwrite or delete anything. Every artifact is public. Disk only grows.

[SETUP.md](SETUP.md) covers each in detail.

## Agents

```bash
cp -r skills/artifact-cli ~/.claude/skills/artifact-cli
```

[`SKILL.md`](skills/artifact-cli/SKILL.md) drives the CLI;
[`REFERENCE.md`](skills/artifact-cli/REFERENCE.md) has the full surface. Both read
`ARTIFACT_ENDPOINT` and `ARTIFACT_TOKEN` from the environment.

## Layout

```
artifact/
├── main.go                   # artifact CLI, built with kong
├── config.go                 # CLI config file and precedence
├── cmd/artifactd/            # server entrypoint, config, shutdown
├── internal/
│   ├── api/                  # routing, auth, handlers, serving
│   ├── store/                # SQLite metadata, durable blob writes
│   ├── render/               # Markdown rendering, page template
│   ├── content/              # filename sanitization, content types
│   └── client/               # HTTP client used by the CLI
├── skills/artifact-cli/      # agent skill
└── docs/                     # HTTP API reference
```

Three dependencies: `kong`, `goldmark`, and `modernc.org/sqlite`. The SQLite
driver is cgo-free, which is what lets `CGO_ENABLED=0` build the static binary
the distroless image needs.

## Development

```bash
go test ./... -race
go test ./internal/render -update   # re-record Markdown golden files
go vet ./... && gofmt -l .
```

Handler tests use a real temp directory and a real SQLite file. The store is
never mocked.
