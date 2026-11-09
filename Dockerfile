# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first: a source-only change should not refetch the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev

# CGO_ENABLED=0 is the whole point of the pure-Go SQLite driver: it produces a
# static binary that runs on distroless/static.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w -X main.Version=${VERSION}" \
        -o /out/artifactd ./cmd/artifactd

# Staged with the right ownership so a fresh named volume mounted at /data
# inherits it and the nonroot user can write there.
RUN mkdir -p /out/data


FROM gcr.io/distroless/static:nonroot

COPY --from=build /out/artifactd /artifactd
COPY --from=build --chown=65532:65532 /out/data /data

ENV ARTIFACT_ADDR=:8080 \
    ARTIFACT_DATA_DIR=/data

EXPOSE 8080
USER 65532:65532

# No shell and no curl in this image, so the health check belongs to the
# orchestrator: GET /healthz over HTTP.
ENTRYPOINT ["/artifactd"]
