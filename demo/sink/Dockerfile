FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go .
RUN CGO_ENABLED=1 go build -o /sink .

FROM alpine:3.19
RUN apk add --no-cache sqlite
COPY --from=builder /sink /sink
EXPOSE 8081
ENTRYPOINT ["/sink"]
