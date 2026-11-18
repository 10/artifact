# Setup

Deploying and operating `artifactd`. Read the origin-separation section before
you pick a hostname — it is the load-bearing security control, and it is a
decision you cannot easily reverse once links are circulating.

---

## 1. Serve artifacts from a dedicated origin

**Deploy `ARTIFACT_BASE_URL` on a hostname that hosts nothing else you care
about.**

Artifact serves uploaded `.html` and `.svg` files byte-for-byte, with no
Content Security Policy. That is deliberate: a restrictive policy would break
the legitimate case — a self-contained HTML report with inline CSS and JS —
which is the whole point of the service. Uploaded HTML runs script in the
origin it is served from, by design.

What makes that safe is that the origin is empty of anything else:

- Artifact never sets a cookie and never accepts authentication from one, so
  there is no session for an uploaded page to steal.
- If the origin hosts nothing but artifacts, same-origin access to "everything
  on this host" means access to public files that anyone could fetch anyway.

Good:

```
https://artifact.example.com     ← nothing else lives here
```

Bad:

```
https://example.com/artifacts/   ← shares an origin with your real app
https://app.example.com          ← shares cookies with your real app
```

If you already serve something on `example.com` with cookies scoped to
`.example.com`, note that a subdomain can read them. Use a separate registrable
domain, or make sure no cookie is set with a `Domain` attribute that covers the
artifact host.

---

## 2. Configuration

Environment variables only. No config file, no flags. The server validates all
of them at startup and exits non-zero with a clear message rather than failing
on the first request.

| Variable | Required | Default | Notes |
| --- | --- | --- | --- |
| `ARTIFACT_TOKEN` | **Yes** | — | Refuses to start if unset or shorter than 32 characters |
| `ARTIFACT_BASE_URL` | **Yes** | — | Public origin, e.g. `https://artifact.example.com`. Used to build every URL in every response; a wrong value yields unreachable links |
| `ARTIFACT_ADDR` | No | `:8080` | Listen address. When unset and `PORT` is set, as Railway and similar platforms do, `:$PORT` |
| `ARTIFACT_DATA_DIR` | No | `/data` | Holds `artifact.db` and `blobs/`. Must be writable at startup |
| `ARTIFACT_DB` | No | `{DATA_DIR}/artifact.db` | Override only for unusual layouts |
| `ARTIFACT_MAX_UPLOAD_SIZE` | No | `104857600` (100 MiB) | Bytes |
| `ARTIFACT_LOG_LEVEL` | No | `info` | `debug`, `info`, `warn`, `error` |

Generate a token:

```console
$ openssl rand -hex 32
```

Rotating it replaces the credential for every agent at once; there is no
per-agent attribution and no selective revocation.

---

## 3. Deploy on Coolify

One container, one persistent volume mounted at `/data`, Traefik terminating
TLS.

1. **New Resource → Application → Dockerfile**, pointed at this repository.
2. **Storage:** add a persistent volume with destination `/data`.
3. **Environment variables:** set `ARTIFACT_TOKEN` and `ARTIFACT_BASE_URL`.
   Leave `ARTIFACT_DATA_DIR` alone; the image already sets it to `/data`.
4. **Domain:** set the FQDN to the same origin as `ARTIFACT_BASE_URL`. Let
   Coolify provision the certificate.
5. **Port:** `8080`.
6. **Health check:** path `/healthz`, port `8080`, expecting `200`.

The image runs as uid `65532` (`distroless/static:nonroot`) and has no shell.
A fresh Docker **named volume** inherits `/data`'s ownership from the image and
works with no extra steps. A **bind mount** does not — chown it on the host
first:

```console
$ sudo chown -R 65532:65532 /srv/artifact/data
```

If `/data` is not writable the server exits at startup with
`data directory "/data" unusable: …`, which is the intended behavior.

### Rate limiting

Rate limiting is the reverse proxy's job, not Artifact's. Traefik middleware,
applied to the artifact router:

```yaml
http:
  middlewares:
    artifact-ratelimit:
      rateLimit:
        average: 20
        burst: 40
```

---

## 4. Deploy with a plain binary and systemd

For anyone not running Coolify.

```console
$ CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /usr/local/bin/artifactd ./cmd/artifactd
$ sudo useradd --system --home /var/lib/artifact --shell /usr/sbin/nologin artifact
$ sudo install -d -o artifact -g artifact -m 0750 /var/lib/artifact
```

`/etc/artifact.env`, mode `0600`, owned by root:

```sh
ARTIFACT_TOKEN=…
ARTIFACT_BASE_URL=https://artifact.example.com
ARTIFACT_ADDR=127.0.0.1:8080
ARTIFACT_DATA_DIR=/var/lib/artifact
```

`/etc/systemd/system/artifactd.service`:

```ini
[Unit]
Description=Artifact file publishing service
After=network-online.target
Wants=network-online.target

[Service]
Type=exec
User=artifact
Group=artifact
EnvironmentFile=/etc/artifact.env
ExecStart=/usr/local/bin/artifactd
Restart=on-failure
RestartSec=2s

# artifactd drains in-flight requests on SIGTERM before closing the database.
KillSignal=SIGTERM
TimeoutStopSec=30s

NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_INET AF_INET6
RestrictNamespaces=true
LockPersonality=true
MemoryDenyWriteExecute=true
ReadWritePaths=/var/lib/artifact

[Install]
WantedBy=multi-user.target
```

```console
$ sudo systemctl daemon-reload
$ sudo systemctl enable --now artifactd
$ curl -s http://127.0.0.1:8080/healthz
{"status":"ok"}
```

Put nginx, Caddy, or Traefik in front for TLS. The only requirements are that
it forwards to `ARTIFACT_ADDR` and that `ARTIFACT_BASE_URL` matches the public
origin. Set a generous `client_max_body_size` (or equivalent) — at least
`ARTIFACT_MAX_UPLOAD_SIZE` — and do not buffer whole request bodies to disk if
you can avoid it.

---

## 5. Backup and restore

**The database and the blob tree must be backed up together and restored
together.** A blob tree newer than the database orphans files; a database newer
than the blob tree produces URLs that `404`.

`VACUUM INTO` takes a consistent snapshot of a live WAL database without
stopping the server. Snapshot the database first, then copy the blobs: the
window between them can only produce blobs the database does not know about,
which is wasted disk rather than a broken link.

```sh
#!/bin/sh
set -eu

DATA_DIR=/var/lib/artifact
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

sqlite3 "$DATA_DIR/artifact.db" "VACUUM INTO '$STAGE/artifact.db'"
cp -a "$DATA_DIR/blobs" "$STAGE/blobs"

tar -C "$STAGE" -czf "/backups/artifact-$(date -u +%Y%m%dT%H%M%SZ).tar.gz" artifact.db blobs
```

Restore, as one unit, with the server stopped:

```console
$ sudo systemctl stop artifactd
$ sudo rm -rf /var/lib/artifact/blobs /var/lib/artifact/artifact.db*
$ sudo tar -C /var/lib/artifact -xzf /backups/artifact-20260726T160211Z.tar.gz
$ sudo chown -R artifact:artifact /var/lib/artifact
$ sudo systemctl start artifactd
```

Removing `artifact.db*` — with the glob — matters: a stale `-wal` or `-shm`
file next to a restored database is a corruption hazard.

Under Docker, run the same steps against the volume, for example with a
throwaway container that mounts it.

---

## 6. Operating notes

### Logs

Structured JSON to stdout via `log/slog`, one line per request with method,
path, status, byte count, duration, and artifact ID. The token is never logged
and request bodies are never logged.

```console
$ journalctl -u artifactd -f -o cat | jq -c 'select(.status >= 400)'
```

### Disk

**Storage grows monotonically.** Mutable artifacts with retained version
history mean disk usage only increases; nothing is reclaimed except by an
explicit `DELETE`. Monitor the data directory and delete artifacts you no
longer need:

```console
$ artifact list --limit 200
$ artifact rm aB3xK9qZ
```

A crash between writing a blob and committing its metadata row leaves an
orphaned blob: wasted bytes, never a broken URL. This asymmetry is intentional.
To find them, compare `blobs/*` directory names against artifact IDs in the
database; any directory with no matching row is safe to remove while the server
is stopped.

### Upgrades

The schema is created idempotently at startup. Upgrading is: pull the new
image or binary, restart. Back up first anyway — see above.

### Boundaries of the security model

An operator must understand these; they are properties of the system, not bugs.

- **The token carries full authority.** Any holder can overwrite or delete
  *any* artifact. There is no per-agent attribution and no selective
  revocation. Artifact suits a personal or small-team deployment and is not
  multi-tenant.
- **Every artifact is public.** Anyone with a link can read it. There is no
  private mode, and the only obstacle to enumeration is 8 random base62
  characters. **Do not upload secrets.**
- **Uploaded HTML and SVG execute script** in the artifact origin. Section 1 is
  what keeps that harmless.
- **Deletion is not shredding.** A `DELETE` removes the blobs and version rows
  and retires the ID, but anything already fetched is already gone.
