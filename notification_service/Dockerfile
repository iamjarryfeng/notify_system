FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN go build -o notification_service ./main.go

FROM alpine:3.19
WORKDIR /app
COPY --from=builder /app/notification_service .
COPY --from=builder /app/migrations ./migrations
EXPOSE 8081
CMD ["./notification_service"]
