# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Copy dependency manifests
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application binary
RUN CGO_ENABLED=0 GOOS=linux go build -o medical-iot-backend ./cmd/server

# Production runner stage
FROM alpine:3.19

WORKDIR /app

# Copy compiled binary from builder
COPY --from=builder /app/medical-iot-backend .

EXPOSE 8080

CMD ["./medical-iot-backend"]
