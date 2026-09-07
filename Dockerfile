FROM golang:1.25.12-alpine AS builder

WORKDIR /app

# Dependencies are vendored (vendor/ is committed) -- github.com/Skookum-
# Infotech/go-rag is a private repo, so a network `go mod download` here
# would fail with no git credentials in this build container. Go
# auto-detects vendor/ + vendor/modules.txt and builds from it with no flag
# needed, as long as the whole tree (including vendor/) is copied first.
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server .

FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /app/server .

EXPOSE 8080

CMD ["./server"]
