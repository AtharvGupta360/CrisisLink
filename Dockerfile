# syntax=docker/dockerfile:1

# ---- build stage ------------------------------------------------------------
# A full Go toolchain image compiles the binaries; none of it ships in the final
# image. This is the whole point of a multi-stage build: the ~800MB toolchain
# stays here, and only the handful of static binaries cross into the runtime.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Copy go.mod/go.sum first and download deps in their own layer. As long as those
# two files don't change, `docker build` reuses the cached module download even
# when application source changes — the slow step runs once, not every build.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO disabled => a fully static binary with no libc dependency, so it can run on
# a `scratch`/minimal base. -ldflags "-s -w" strips the symbol table and DWARF
# debug info, shrinking each binary by a few MB. One build per entrypoint.
ARG VERSION=dev
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/server   ./cmd/server  && \
    go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/relay    ./cmd/relay   && \
    go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/consumer ./cmd/consumer && \
    go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/gateway  ./cmd/gateway && \
    go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/migrate  ./cmd/migrate

# ---- runtime stage ----------------------------------------------------------
# alpine (not scratch) for two reasons: a shell for debugging, and ca-certificates
# so any future outbound TLS (managed Redis/Kafka) works. Still only ~15MB.
FROM alpine:3.20 AS runtime

RUN apk add --no-cache ca-certificates && \
    adduser -D -u 10001 crisislink

WORKDIR /app
# Copy every binary; which one runs is chosen by the compose/k8s command, so one
# image serves the api, relay, consumer and gateway — they share a codebase.
COPY --from=build /out/ /app/
COPY docker-entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

# Run as a non-root user. A compromised process should not be root inside the
# container; this is defense in depth that costs nothing.
USER crisislink

# Default: migrate then serve the API. Other services override the entrypoint
# (e.g. `--entrypoint /app/relay`), which is why every binary ships in the image.
EXPOSE 8080
ENTRYPOINT ["/app/entrypoint.sh"]
