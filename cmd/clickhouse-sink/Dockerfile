# The ClickHouse sink service image: one static binary, configured by
# environment variables only, running as an unprivileged user.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go main.go
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/clickhouse-sink .

FROM alpine:3.23
RUN addgroup -g 10001 sink && adduser -D -u 10001 -G sink sink
COPY --from=build /out/clickhouse-sink /usr/local/bin/clickhouse-sink
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/clickhouse-sink"]
