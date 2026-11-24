# The webhook gateway service image: one static binary, configured by
# environment variables plus a mounted subscriptions file, running as an
# unprivileged user.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/webhook-gateway .

FROM alpine:3.23
RUN addgroup -g 10001 gateway && adduser -D -u 10001 -G gateway gateway
COPY --from=build /out/webhook-gateway /usr/local/bin/webhook-gateway
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/webhook-gateway"]
