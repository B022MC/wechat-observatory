# Deployment

本文档说明如何部署 `wechat-observatory`。公开部署时请使用 HTTPS、反向代理和强密码。

## 环境变量

| 变量 | 必填 | 说明 |
| --- | --- | --- |
| `BRIDGE_HTTP_ADDR` | 否 | HTTP 监听地址，默认 `:8088` |
| `BRIDGE_ADMIN_PASSWORD` | 是 | Web 管理台和管理 API 密码 |
| `BRIDGE_DEFAULT_DEVICE` | 否 | 默认设备名 |
| `BRIDGE_DEVICES` | 无 MySQL 时必填 | 初始设备，格式 `device|wxid|display|timeout`，`wxid` 可留空 |
| `BRIDGE_API_KEYS` | 无 MySQL 时建议填 | 初始 API Key，格式 `key|device|nickname` |
| `BRIDGE_MYSQL_DSN` | 生产建议填 | MySQL DSN |
| `BRIDGE_MYSQL_AUTO_MIGRATE` | 否 | 是否启动时自动迁移，生产建议 `false` |

## Docker Compose

复制并编辑配置：

```bash
cp deploy/docker/.env.example deploy/docker/.env
```

至少修改：

```text
BRIDGE_ADMIN_PASSWORD=your-strong-admin-password
MYSQL_PASSWORD=your-strong-mysql-password
MYSQL_ROOT_PASSWORD=your-strong-root-password
```

构建镜像并启动服务：

```bash
docker compose -f deploy/docker/docker-compose.yml up -d --build
```

Compose 会启动 MySQL，等待数据库健康，执行一次 `gateway-db` 初始化任务，然后启动网关。

检查状态：

```bash
curl -fsS http://127.0.0.1:8088/healthz
```

管理台地址：

```text
https://<server-host>/admin/
```

正式版手机模块固定使用 `https://47.108.171.42/observatory`。生产部署必须
保证该地址由 Caddy 转发到内部 Observatory；主机上的 8088 只绑定本机，
不能再作为公网入口。

## 更新版本

```bash
docker compose -f deploy/docker/docker-compose.yml up -d --build
```

## k3s 参考

`deploy/k3s` 提供基础示例：

- `namespace.yaml`
- `config.example.yaml`
- `secrets.example.yaml`
- `deployment.yaml`
- `service.yaml`

使用前复制示例文件并改成真实配置：

```bash
cp deploy/k3s/config.example.yaml deploy/k3s/config.yaml
cp deploy/k3s/secrets.example.yaml deploy/k3s/secrets.yaml
```

不要提交真实的 `config.yaml` 和 `secrets.yaml`。

## 媒体文件

模块上传的 `media_base64` 会在入口处丢弃；当前部署不会写附件文件，也不会生成可下载的 `media_url`。因此不需要媒体卷、对象存储或媒体备份策略。

## 网络建议

- 不要把管理台裸露到公网。
- 至少放在 VPN、内网、堡垒机或反向代理鉴权之后。
- API Key 泄露后应立即停用或删除。
- 删除 API Key 会注销对应模块身份，手机端需要填写新的 API Key 后重新注册。
