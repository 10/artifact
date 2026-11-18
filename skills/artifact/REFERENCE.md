# Artifact reference

Consulted on demand. The operating loop is in [SKILL.md](SKILL.md).

## Commands

| Command | Does |
| --- | --- |
| `artifact <file>` | Publish; prints the URL. `up` is the default command, so the subcommand is optional |
| `artifact up <file>` | Same, and the form that accepts `--slug` |
| `artifact update <id> <file>` | Publish new content as the next version; the URL is unchanged |
| `artifact list` | Recent artifacts, newest first, as a table |
| `artifact info <id>` | Metadata and full version history |
| `artifact rm <id>` | Delete the artifact, every version, and every blob |
| `artifact open <id>` | Open in the operator's browser — for a human at a terminal, not for an agent |
| `artifact version` | Version, commit, build date. Needs no endpoint or token |

`<id>` accepts an ID or a slug everywhere.

## Flags

| Flag | Applies to | Notes |
| --- | --- | --- |
| `--slug SLUG` | `up` | Publish at a stable path instead of a random ID |
| `--filename NAME` | `up`, `update` | Override the recorded filename, which is what drives content type |
| `--limit N` | `list` | Default 50, server caps at 200 |
| `--cursor C` | `list` | Continue from the previous page's cursor |
| `--json` | all | Emit the API response verbatim on stdout instead of human output |
| `--quiet` | all | Suppress every stderr diagnostic, errors included |
| `--endpoint URL` | all | `ARTIFACT_ENDPOINT` |
| `--token TOKEN` | all | `ARTIFACT_TOKEN` |
| `--config PATH` | all | Default `~/.config/artifact/config.toml` |

Precedence is flag, then environment, then config file.

Use `--json` when a field other than the URL is needed — `id`, `version`,
`sha256`, `size`:

```bash
ID=$(artifact --json report.html | jq -r '.id')
```

## Content types

The extension is matched first. An unknown extension falls back to sniffing the
first 512 bytes, then to `application/octet-stream`, which downloads.

| Extensions | Served as | Behavior |
| --- | --- | --- |
| `.md` `.markdown` | `text/html` | Rendered: GFM tables, task lists, code blocks, strikethrough, autolinks |
| `.html` `.htm` | `text/html` | The bytes exactly as uploaded |
| `.png` `.jpg` `.jpeg` `.gif` `.webp` `.avif` `.svg` | `image/*` | Inline |
| `.mp4` `.webm` `.mov` | `video/*` | Inline, seekable |
| `.mp3` `.wav` `.m4a` | `audio/*` | Inline, seekable |
| `.pdf` | `application/pdf` | Inline |
| `.txt` `.log` `.json` `.yaml` `.yml` `.toml` `.csv` `.xml` `.go` `.py` `.ts` `.js` `.sh` `.sql` `.rs` `.c` `.h` | `text/plain` | Inline |
| anything else | `application/octet-stream` | Downloads |

## URL scheme

Given a published URL like `https://host/aB3xK9qZ`:

| URL | Serves |
| --- | --- |
| `/aB3xK9qZ` | Latest version, rendered |
| `/aB3xK9qZ/raw` | Latest version, never rendered |
| `/aB3xK9qZ@2` | Version 2, frozen; identical bytes forever |
| `/aB3xK9qZ@2/raw` | Version 2, never rendered |
| `/aB3xK9qZ/report.md` | Latest version, but only if the filename matches exactly |

Rendering is a Markdown-only step, so `/raw` only changes anything for `.md`:
it returns the source as `text/plain` instead of a rendered page. Every other
type is already served as its own bytes, and `/raw` returns exactly the same
response.

Link a human to `/aB3xK9qZ`. Use `/raw` when a machine needs Markdown source,
and `@n` when a reviewer needs bytes that cannot shift under them.

## Without the CLI

For environments where installing the binary is not worth it. One request
creates an artifact — no session, no handshake.

```bash
FILE="$1"
NAME=$(jq -rn --arg f "$(basename "$FILE")" '$f|@uri')
curl -sS --fail-with-body -X POST \
  -H "Authorization: Bearer $ARTIFACT_TOKEN" \
  --data-binary "@$FILE" \
  "$ARTIFACT_ENDPOINT/api/upload?filename=$NAME" \
  | jq -r '.url'
```

`NAME` is encoded, so a screenshot called
`Screenshot 2026-11-17 at 9.41.07 PM.png` still uploads. Add
`&slug=daily-report` for a stable URL. To publish a new version, `PUT` to
`/api/artifacts/{id}` with the same body form.

## API error codes

Errors are JSON with a stable `error.code`. Branch on the code, not the prose.

```json
{ "error": { "code": "slug_taken", "message": "slug already in use" } }
```

| Code | Status | Cause |
| --- | --- | --- |
| `bad_request` | 400 | Missing or unusable `filename`, malformed slug, empty file |
| `unauthorized` | 401 | Token missing or wrong |
| `not_found` | 404 | No such artifact, version, or filename |
| `slug_taken` | 409 | Slug in use, including by a deleted artifact |
| `payload_too_large` | 413 | Larger than the server's upload cap |
| `internal` | 500 | Server-side; one retry is reasonable |

The CLI maps these to exit codes — see the failure table in SKILL.md.
