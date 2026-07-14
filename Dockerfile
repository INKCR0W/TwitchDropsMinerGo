FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/miner-server ./cmd/miner-server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/miner-server /usr/local/bin/miner-server
HEALTHCHECK --interval=60s --timeout=10s --start-period=120s --retries=3 \
  CMD ["miner-server", "-healthcheck", "-runtime-dir", "/data"]
ENTRYPOINT ["miner-server", "-runtime-dir", "/data"]
