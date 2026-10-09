# ---------- 1. 前端构建 ----------
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
# 必须包含 optional 依赖：esbuild / rollup 的平台二进制在 optionalDependencies 里。
RUN npm ci --include=optional --no-audit --no-fund || npm install --include=optional --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---------- 2. 后端构建 ----------
FROM golang:1.26-alpine AS api
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
# CGO_ENABLED=0：依赖 modernc.org/sqlite（纯 Go），无需 gcc / build-base。
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/passpal ./cmd/passpal

# ---------- 3. 运行 ----------
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata wget \
 && addgroup -S -g 10001 passpal \
 && adduser -S -D -H -u 10001 -G passpal passpal

WORKDIR /app
COPY --from=api /out/passpal /app/passpal

USER 10001:10001
ENV APP_ENV=production \
    BIND_ADDR=0.0.0.0 \
    PORT=3000 \
    DATABASE_PATH=/app/data/database.sqlite \
    BACKUP_DIR=/app/backups

VOLUME ["/app/data", "/app/backups"]
EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:3000/api/health || exit 1

ENTRYPOINT ["/app/passpal", "serve"]
