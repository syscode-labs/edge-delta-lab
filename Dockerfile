# syntax=docker/dockerfile:1
# Multi-stage build: the same signed-chunk code base produces the sender/hub
# daemon (edgelab) and the lightweight client binary (also edgelab; the client
# is the sync/watch side of the one binary). Static, CGO-free, non-root.
FROM --platform=$BUILDPLATFORM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY hubclient/ hubclient/
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags "-s -w" -o /out/edgelab ./cmd/edgelab
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags "-s -w" -o /out/edgelab-tui ./cmd/edgelab-tui
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags "-s -w" -o /out/edgelab-exporter ./cmd/edgelab-exporter

FROM alpine:3.20 AS daemon
RUN addgroup -S edgelab && adduser -S -G edgelab edgelab \
    && mkdir -p /state && chown edgelab:edgelab /state
COPY --from=build /out/edgelab /usr/local/bin/edgelab
COPY --from=build /out/edgelab-tui /usr/local/bin/edgelab-tui
COPY --from=build /out/edgelab-exporter /usr/local/bin/edgelab-exporter
USER edgelab
WORKDIR /state
EXPOSE 8080
# State (event log, admin socket, receipts) lives here. The published origin
# itself is mounted read-only; the daemon never writes the origin.
VOLUME /state
ENTRYPOINT ["/usr/local/bin/edgelab"]
CMD ["serve", "--root", "/origin", "--listen", "0.0.0.0:8080", \
     "--admin-socket", "/state/admin.sock"]

# Client stage: same binary, no exposed port, dry-run by default (Docker is
# never touched without an explicit action mode).
FROM alpine:3.20 AS client
RUN addgroup -S edgelab && adduser -S -G edgelab edgelab \
    && mkdir -p /state && chown edgelab:edgelab /state
COPY --from=build /out/edgelab /usr/local/bin/edgelab
COPY --from=build /out/edgelab-tui /usr/local/bin/edgelab-tui
COPY --from=build /out/edgelab-exporter /usr/local/bin/edgelab-exporter
USER edgelab
WORKDIR /state
VOLUME /state
# No default subcommand: the client is dry-run by constitution (Docker is
# never implicit), so the container prints usage until an operator picks
# sync/watch/conf explicitly.
ENTRYPOINT ["/usr/local/bin/edgelab"]
