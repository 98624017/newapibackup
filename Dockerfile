FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/backup-worker ./cmd/backup-worker

FROM alpine:3.22

RUN apk add --no-cache ca-certificates postgresql-client

COPY --from=builder /out/backup-worker /usr/local/bin/backup-worker

USER nobody:nobody

ENTRYPOINT ["/usr/local/bin/backup-worker"]
