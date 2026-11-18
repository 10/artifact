# HTTP API

All `/api/*` endpoints require `Authorization: Bearer $ARTIFACT_TOKEN`.
All artifact-serving routes are public and unauthenticated.

A `401` is returned before the request body is read, so a bad token never
consumes an upload.

---

## `POST /api/upload`

Creates a new artifact. Accepts the file two ways.

**Raw body** — preferred for agents and `curl`:

```console
$ curl -sS -X POST \
    -H "Authorization: Bearer $ARTIFACT_TOKEN" \
    --data-binary @report.html \
    "$ARTIFACT_ENDPOINT/api/upload?filename=report.html"
```

**Multipart** (`multipart/form-data`): the file in the `file` field, with
optional `filename` and `slug` fields.

| Parameter | Where | Required | Notes |
| --- | --- | --- | --- |
| `filename` | query or form | Yes | Used for content-type resolution and `Content-Disposition`. In a multipart request, falls back to the file part's own filename. |
| `slug` | query or form | No | See the URL scheme in the README. |

An explicit `filename` field or query parameter wins over the multipart part's
filename. The filename is sanitized before storage: `filepath.Base`, control
characters and path separators stripped, `.`/`..`/empty rejected, capped at 255
bytes. It is stored in SQLite only and never appears in a filesystem path.

Response `201 Created`:

```json
{
  "id": "aB3xK9qZ",
  "slug": null,
  "url": "https://artifact.example.com/aB3xK9qZ",
  "raw_url": "https://artifact.example.com/aB3xK9qZ/raw",
  "version": 1,
  "version_url": "https://artifact.example.com/aB3xK9qZ@1",
  "filename": "report.html",
  "content_type": "text/html; charset=utf-8",
  "size": 20481,
  "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "created_at": "2026-07-26T16:02:11Z",
  "updated_at": "2026-07-26T16:02:11Z"
}
```

When the artifact has a slug, `url`, `raw_url`, and `version_url` are built
from the slug. The ID form always resolves as well.

A `201` means the bytes are fsynced to disk and the metadata row is committed.

---

## `PUT /api/artifacts/{id}`

Replaces content, creating version `n+1`. `{id}` may be an ID or a slug. Same
request forms as upload; a `slug` parameter is ignored, because a slug belongs
to the artifact rather than to a version.

Response `200 OK` with the same body shape, `version` incremented and
`updated_at` refreshed. The artifact's URL is unchanged and prior `@n` URLs
return their original bytes forever.

---

## `DELETE /api/artifacts/{id}`

Deletes the artifact, all versions, and all blobs. Response `204 No Content`
with no body. Deleting an already-deleted artifact is `404`.

The artifact's row is retained as a tombstone so its ID and slug stay retired.

---

## `GET /api/artifacts`

Lists artifacts, newest-updated first.

| Parameter | Default | Notes |
| --- | --- | --- |
| `limit` | 50 | Values above 200 are capped at 200. Non-numeric or `< 1` is `400`. |
| `cursor` | — | Opaque cursor from the previous response. |

```json
{
  "artifacts": [ { "id": "aB3xK9qZ", "…": "same shape as upload" } ],
  "next_cursor": "eyJ1IjoiMjAyNi0wNy0yNlQxNjowMjoxMVoiLCJpIjoiYUIzeEs5cVoifQ"
}
```

`next_cursor` is `null` on the last page.

---

## `GET /api/artifacts/{id}`

Metadata for one artifact, including full version history. `{id}` may be an ID
or a slug.

```json
{
  "id": "aB3xK9qZ",
  "slug": null,
  "url": "https://artifact.example.com/aB3xK9qZ",
  "latest_version": 2,
  "created_at": "2026-07-26T16:02:11Z",
  "updated_at": "2026-07-26T17:14:03Z",
  "versions": [
    { "n": 1, "filename": "report.html", "content_type": "text/html; charset=utf-8",
      "size": 20481, "sha256": "e3b0…", "created_at": "2026-07-26T16:02:11Z",
      "url": "https://artifact.example.com/aB3xK9qZ@1" },
    { "n": 2, "filename": "report.html", "content_type": "text/html; charset=utf-8",
      "size": 21033, "sha256": "a94a…", "created_at": "2026-07-26T17:14:03Z",
      "url": "https://artifact.example.com/aB3xK9qZ@2" }
  ]
}
```

---

## `GET /healthz`

Unauthenticated. Returns `200 {"status":"ok"}` when the database answers a ping
and the data directory is writable; `503 {"status":"unavailable"}` otherwise.

---

## Errors

Every non-2xx API response uses this shape:

```json
{ "error": { "code": "not_found", "message": "artifact not found" } }
```

| Status | `code` | Cause |
| --- | --- | --- |
| `400` | `bad_request` | Missing `filename`, malformed filename, malformed or reserved slug, empty body, bad `limit` or `cursor` |
| `401` | `unauthorized` | Missing, malformed, or incorrect token |
| `404` | `not_found` | No such artifact, version, or filename |
| `409` | `slug_taken` | Slug already in use, including by a deleted artifact |
| `413` | `payload_too_large` | Body exceeds `ARTIFACT_MAX_UPLOAD_SIZE` |
| `500` | `internal` | Unexpected server error; details are logged, not returned |

`code` is stable, so a client can branch on it rather than parsing prose.

Public artifact routes are not part of the JSON API and answer with plain text
on failure.

---

## Public routes

See the URL scheme table in the [README](../README.md). Every artifact
response carries `X-Content-Type-Options: nosniff` and a `Content-Disposition`
header (`inline` for everything previewable, `attachment` for the
`application/octet-stream` floor), encoded per RFC 6266.

Bytes served as uploaded carry an `ETag` of their `sha256`. URLs without
`@n` also send `Cache-Control: no-cache`, so a browser checks back after an
update rather than showing the copy it already has.
