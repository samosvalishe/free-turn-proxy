FROM golang:1.25-alpine AS builder

WORKDIR /build

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o server ./cmd/server

FROM alpine:3.23

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY docker/entrypoint.sh .
COPY --from=builder /build/server .
RUN sed -i 's/\r$//' entrypoint.sh && chmod +x entrypoint.sh

EXPOSE 56000/udp
EXPOSE 56000/tcp

ENTRYPOINT ["./entrypoint.sh"]
