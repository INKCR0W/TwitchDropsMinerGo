FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/miner-server ./cmd/miner-server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/miner-server /usr/local/bin/miner-server
ENTRYPOINT ["miner-server", "-runtime-dir", "/data"]
