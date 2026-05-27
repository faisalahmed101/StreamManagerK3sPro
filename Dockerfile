FROM golang:1.26 AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /app/stream .

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/stream /app/stream

EXPOSE 9090
CMD ["/app/stream"]