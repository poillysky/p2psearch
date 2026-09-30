# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY . .
RUN go mod tidy \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/p2psearch ./cmd/p2psearch

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -H -u 10001 app
WORKDIR /app
COPY --from=builder /out/p2psearch /app/p2psearch
COPY config.yaml /app/config.yaml
USER app
ENV HTTP_ADDR=:8080 \
    CONFIG_PATH=/app/config.yaml \
    GOED2K_KAD=1 \
    GOED2K_UPNP=0 \
    GOED2K_NO_STATE=1
EXPOSE 8080 4661 4662/udp
ENTRYPOINT ["/app/p2psearch"]
