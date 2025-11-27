FROM golang:1.25.3 AS build
WORKDIR /app
ENV GOTOOLCHAIN=auto

# Сначала зависимости, чтобы кэшировались слои.
COPY go.mod go.sum ./
COPY vendor ./vendor

# Код.
COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg
COPY config ./config

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOFLAGS=-mod=vendor \
    go build -o /usr/local/bin/timemachine ./cmd/timemachine

FROM alpine:3.18
RUN apk add --no-cache ca-certificates curl
COPY --from=build /usr/local/bin/timemachine /usr/local/bin/timemachine
COPY config ./config

EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/timemachine"]
