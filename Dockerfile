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

# 3. Runtime: distroless, non-root. Ping uses unprivileged ICMP sockets, so
# no CAP_NET_RAW is needed (the host must allow it via ping_group_range,
# which Docker does by default).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/upsera /upsera
USER nonroot:nonroot
ENV PORT=3080
EXPOSE 3080
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD ["/upsera", "healthcheck"]
ENTRYPOINT ["/upsera"]
