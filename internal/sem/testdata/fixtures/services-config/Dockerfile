FROM golang:1.22 AS builder
WORKDIR /src
RUN go build ./...

FROM alpine:3.20 AS runtime
COPY --from=builder /src/app /app
ENTRYPOINT ["/app"]
