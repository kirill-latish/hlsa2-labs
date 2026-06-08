# Shared Dockerfile for every cmd/* binary in lab 4-3.
# Compose passes BIN as a build arg, e.g. BIN=loadgen.

ARG GO_VERSION=1.22
FROM golang:${GO_VERSION}-alpine AS build
ARG BIN=loadgen
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/app ./cmd/${BIN}

FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget curl
WORKDIR /app
COPY --from=build /out/app /app/app
ENTRYPOINT ["/app/app"]
