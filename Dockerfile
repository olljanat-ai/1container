FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/container-hub ./cmd/server/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates curl
COPY --from=builder /bin/container-hub /usr/local/bin/container-hub
COPY ui/ /app/ui/
WORKDIR /app
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
  CMD curl -f http://localhost:8080/healthz || exit 1
ENTRYPOINT ["container-hub"]
