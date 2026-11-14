# ---------- frontend build ----------
FROM node:22-alpine AS web
WORKDIR /web
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm install --registry=https://registry.npmmirror.com
COPY frontend/ ./
RUN npm run build

# ---------- backend build ----------
FROM golang:1.26-alpine AS api
WORKDIR /src
ENV GOPROXY=https://goproxy.cn,direct GOFLAGS=-mod=mod CGO_ENABLED=0
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN go build -trimpath -ldflags="-s -w" -o /out/rt-server ./cmd/server && \
    go build -trimpath -ldflags="-s -w" -o /out/rt-seed ./cmd/seed

# ---------- runtime ----------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
WORKDIR /app
COPY --from=api /out/rt-server /usr/local/bin/rt-server
COPY --from=api /out/rt-seed /usr/local/bin/rt-seed
COPY --from=web /backend/web/dist /app/web/dist
USER app
ENV ADDR=:8080 WEB_DIR=/app/web/dist DEMO_SEED=1
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/rt-server"]
