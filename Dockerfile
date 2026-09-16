FROM node:20-alpine AS web-build

WORKDIR /src/web/admin
COPY web/admin/package*.json ./
RUN npm ci
COPY web/admin ./
RUN npm run build

FROM node:20-alpine AS device-web-build

WORKDIR /src/web/device
COPY web/device/package*.json ./
RUN npm ci
COPY web/device ./
COPY web/admin/src /src/web/admin/src
RUN ln -s /src/web/device/node_modules /src/web/admin/node_modules
RUN npm run build

FROM golang:1.24.2-alpine AS build

ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web-build /src/internal/bridge/admin_dist ./internal/bridge/admin_dist
COPY --from=device-web-build /src/internal/bridge/device_dist ./internal/bridge/device_dist
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/wechat-observatory ./cmd/bridge
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/wechat-observatory-db ./cmd/bridge-db

FROM alpine:3.21

RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
	&& apk add --no-cache ca-certificates wget
RUN adduser -D -H -u 10001 appuser
COPY --from=build /out/wechat-observatory /usr/local/bin/wechat-observatory
COPY --from=build /out/wechat-observatory-db /usr/local/bin/wechat-observatory-db

USER appuser
EXPOSE 8088
ENTRYPOINT ["/usr/local/bin/wechat-observatory"]
