FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN go build -o event_processor ./main.go

FROM alpine:3.19
WORKDIR /app
COPY --from=builder /app/event_processor .
COPY --from=builder /app/migrations ./migrations
EXPOSE 8080
CMD ["./event_processor"]
