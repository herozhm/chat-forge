# ─── 构建阶段 ───
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o chat-forge-server .

# ─── 运行阶段 ───
FROM alpine:3.20
RUN apk add --no-cache tzdata ca-certificates
WORKDIR /app
COPY --from=builder /app/chat-forge-server .
ENV DB_PATH=/data/chat-forge.db
VOLUME ["/data"]
EXPOSE 8080
CMD ["./chat-forge-server"]
