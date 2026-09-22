# syntax=docker/dockerfile:1

FROM golang:1.25-bookworm AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=1
RUN go build -trimpath -ldflags "-s -w -X main.Version=${VERSION}" \
    -o /out/whatsapp-mcp ./cmd/whatsapp-mcp

FROM debian:bookworm-slim

# ffmpeg: only needed by send_audio_message for non-Opus input (see
# internal/media/audio.go); the tool degrades to a clear error without it,
# so it's not optional in this image but the binary itself would still run.
# ca-certificates: TLS to WhatsApp's servers.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        ffmpeg \
    && rm -rf /var/lib/apt/lists/*

RUN useradd -r -u 10001 -m app
COPY --from=build /out/whatsapp-mcp /usr/local/bin/whatsapp-mcp

USER app
WORKDIR /home/app
RUN mkdir -p /home/app/store

# Loopback-only by default, matching the binary's own default. Reaching this
# from outside the container (docker run -p) needs the app's own remote-bind
# opt-in: override CMD with `-addr 0.0.0.0:8765 -allow-remote` and set
# WHATSAPP_MCP_TOKEN — see README.md.
EXPOSE 8765
VOLUME ["/home/app/store"]

ENTRYPOINT ["whatsapp-mcp", "-store", "/home/app/store"]
CMD ["serve"]
