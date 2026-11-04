FROM golang:1.22-alpine AS builder
WORKDIR /build
COPY main.go .
RUN go mod init healthserver && \
    go mod edit -go=1.22 && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o healthserver .

FROM scratch
COPY --from=builder /build/healthserver /healthserver
ENTRYPOINT ["/healthserver"]
