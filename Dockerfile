# Multi-service Dockerfile using build arg to select service
FROM golang:1.22-alpine AS builder

ARG SERVICE
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/service ./cmd/${SERVICE}/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /bin/service /usr/local/bin/service
COPY --from=builder /app/policies /app/policies
ENTRYPOINT ["/usr/local/bin/service"]
