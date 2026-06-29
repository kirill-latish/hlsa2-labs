# syntax=docker/dockerfile:1.7
FROM golang:1.22-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod ./
RUN go mod download || true
COPY . .
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=mod go build -trimpath -ldflags="-s -w" \
    -o /out/outbox-relay ./cmd/outbox-relay

FROM alpine:3.20
RUN apk add --no-cache wget ca-certificates && update-ca-certificates
COPY --from=build /out/outbox-relay /usr/local/bin/outbox-relay
EXPOSE 9102
ENTRYPOINT ["/usr/local/bin/outbox-relay"]
