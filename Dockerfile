FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/container-hub ./cmd/server/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/container-hub /usr/local/bin/container-hub
COPY ui/ /app/ui/
WORKDIR /app
EXPOSE 8080
ENTRYPOINT ["container-hub"]
