# Multi-stage build for Coolify (port 8080).
# Templates and seed data are embedded in the binary; mount a persistent
# volume at /root/data so itineraries added via API/MCP survive redeploys.
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod ./
# COPY go.sum ./   # no third-party dependencies
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o app .

FROM alpine:3.20
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /root/
RUN mkdir -p /root/data
COPY --from=builder /app/app .
EXPOSE 8080
ENV PORT=8080 \
    DATA_FILE=/root/data/itineraries.json
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:${PORT}/healthz" >/dev/null || exit 1
CMD ["./app"]
