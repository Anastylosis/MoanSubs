# syntax=docker/dockerfile:1.7

# GO_VERSION picks the builder image. CI passes go.mod's `go` directive; the
# default below is only the fallback for a plain `docker build .`, and it may
# lag go.mod: GOTOOLCHAIN=auto in the builder makes Go fetch the toolchain
# go.mod asks for, so a dependency bump that raises the directive builds
# without anyone editing this file.
ARG GO_VERSION=1.25.0
ARG ALPINE_VERSION=3.21

# ---- build ----
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

# The golang image sets GOTOOLCHAIN=local, which refuses a go.mod newer
# than the image instead of fetching the toolchain it names.
ENV GOTOOLCHAIN=auto

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
      -o /out/moansubs ./cmd/moansubs

# ---- runtime ----
FROM alpine:${ALPINE_VERSION}

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S moansubs && adduser -S -G moansubs -h /home/moansubs moansubs

COPY --from=builder /out/moansubs /usr/local/bin/moansubs
# The commented settings reference, so an operator running only the deploy
# kit can read it without the repository:
#   docker compose exec server cat /etc/moansubs/config.example.yaml
COPY config.example.yaml /etc/moansubs/config.example.yaml

USER moansubs
WORKDIR /home/moansubs

EXPOSE 8080

# /healthz pings Postgres (internal/api.handleHealthz) and only reports
# 200 when that succeeds, so this also catches a server that's up but has
# lost its database, surfaced in `docker ps`/`docker inspect` like any
# other container health status.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O - http://localhost:8080/healthz || exit 1

ENTRYPOINT ["moansubs"]
CMD ["serve"]
