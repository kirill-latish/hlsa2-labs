# Single multi-stage Dockerfile shared by every cmd/* binary in this lab.
# Compose passes BIN as a build arg, e.g. BIN=payment-svc.

ARG GO_VERSION=1.22
FROM golang:${GO_VERSION}-alpine AS build
ARG BIN=orchestrator
WORKDIR /src

# Cache the module download.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/app ./cmd/${BIN}

FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=build /out/app /app/app
ENTRYPOINT ["/app/app"]
