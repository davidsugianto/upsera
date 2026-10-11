# syntax=docker/dockerfile:1

# 1. Dashboard: Vite builds web/dist/app, which the Go build embeds.
FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 2. Server: a static binary with the dashboard and tzdata embedded.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist/app ./web/dist/app
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/upsera ./cmd/upsera
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/upsera-probe ./cmd/upsera-probe
# Distroless has no shell, so create the probe's data directory here and copy
# it in owned by nonroot (a fresh named volume inherits that ownership).
RUN mkdir -p /out/data

# 3. Remote probe agent (`docker build --target probe`). It keeps its
# credential in /data, so mount a volume there. Same ping note as below.
FROM gcr.io/distroless/static-debian12:nonroot AS probe
COPY --from=build /out/upsera-probe /upsera-probe
COPY --from=build --chown=nonroot:nonroot /out/data /data
USER nonroot:nonroot
ENV DATA_DIR=/data
VOLUME /data
ENTRYPOINT ["/upsera-probe"]

# 4. Server runtime (keep this the LAST stage so a plain `docker build .`
# builds the server): distroless, non-root. Ping uses unprivileged ICMP sockets, so
# no CAP_NET_RAW is needed (the host must allow it via ping_group_range,
# which Docker does by default).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/upsera /upsera
USER nonroot:nonroot
ENV PORT=3080
EXPOSE 3080
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD ["/upsera", "healthcheck"]
ENTRYPOINT ["/upsera"]
