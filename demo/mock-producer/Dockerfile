FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go .
RUN go build -o /mock-producer .

FROM alpine:3.19
COPY --from=builder /mock-producer /mock-producer
ENTRYPOINT ["/mock-producer"]
