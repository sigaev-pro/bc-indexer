# The image of the real-network extractor adapter: one static binary built
# from the repository-local modules the go.mod replaces point at, so the
# build context is the repository root (see docker-compose.sepolia.yml).
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY cmd/extractor-rpc/go.mod cmd/extractor-rpc/go.sum ./cmd/extractor-rpc/
COPY core core
COPY gateway gateway
COPY protocol protocol
COPY cmd/extractor-rpc/main.go ./cmd/extractor-rpc/
WORKDIR /src/cmd/extractor-rpc
RUN go mod download && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/extractor-rpc .

FROM alpine:3.23
COPY --from=build /out/extractor-rpc /usr/local/bin/extractor-rpc
USER 65534
ENTRYPOINT ["/usr/local/bin/extractor-rpc"]
