# WeChat Observatory

## 友链

- [LINUX DO 社区](https://linux.do/)
## 项目意义
其实就是因为逆向出来的ipad协议、车载协议等等，容易风控被下线 而且不能再次扫码登录 只能使用wx app 才做了这个项目。这样就算被风控也能继续使用微信、机器人 直接控制的wxapp 就不需要再去扫码登陆其它东西。最重要的是被冻结了依然能识别指令，只是不能回复消息。
## 截图

### Web 管理台

![API Key 与设备管理](docs/images/readme-web-admin-api-key-status.png)

![实时面板 1](docs/images/readme-web-admin-dashboard-1.png)

![实时面板 2](docs/images/readme-web-admin-dashboard-2.png)

### Android / LSPosed 模块

![微信网关模块设置](docs/images/readme-mobile-gateway-settings.jpg)

![模块列表](docs/images/readme-mobile-modules.jpg)

### 小米设备解锁

如果你手上是小米机型，先把解锁这一步走通，给你们指一条明路：[Linuxoid-cn/Mi8G3-Unlocker](https://github.com/Linuxoid-cn/Mi8G3-Unlocker)。

WeChat Observatory（微信看台）是一个通过 Android LSPosed 模块接入微信的网关控制台。手机模块负责在微信进程内观察消息、同步联系人、执行发送；服务端负责 API Key 身份、当前微信账号绑定、消息持久化、出站队列和 Web 管理台。

当前 Android LSPosed 模块面向微信 Android `8.0.75` 适配。微信内部实现随版本变化，升级微信后需要重新验证消息观察、联系人同步、媒体上传和发送路径。

```text
微信消息 -> LSPosed 模块 -> 网关入库和实时推送 -> Web 管理台
Web 手动发送 -> 网关出站队列/WebSocket -> LSPosed 模块 -> 微信
```

## 功能

- API Key 管理：生成、停用、启用、删除。删除 key 后对应模块会被注销。
- 模块注册：模块自动识别当前微信 `wxid`，不需要用户手动填写。
- 当前账号隔离：切换微信后，同一个 API Key 更新当前绑定，旧账号消息不会混到当前聊天列表。
- 通讯录同步：支持好友、群聊、文件传输助手。
- 消息观测：文本、图片、语音、视频、文件等消息类型均可记录；附件字节上传后可在 Web 中预览或播放。
- 手动发送：Web 管理台发送文本，手机模块从队列取消息并在微信内发送。
- 实时刷新：Web 管理台通过 SSE/WebSocket/轮询组合展示实时状态和消息。
- Docker 部署：提供单机 Docker Compose 示例和 k3s 参考配置。

## 项目结构

```text
cmd/bridge                 网关服务入口
cmd/bridge-db              数据库创建、迁移、种子和检查工具
internal/bridge            HTTP API、模块注册、事件、出站队列、Web 静态资源
internal/storage/mysql     MySQL 表结构和持久化实现
android-module             LSPosed 微信模块
web/admin                  React + shadcn 风格管理台源码
internal/bridge/admin_dist Web 管理台构建后的嵌入资源
deploy/docker              Docker Compose 部署示例
deploy/k3s                 k3s 部署参考
docs                       详细文档
```

## 快速开始

### 1. 准备环境

- Go 1.24.2+
- Node.js 20+ 和 npm
- MySQL 8+，生产建议使用 MySQL
- Android Studio / Android SDK，用于构建 LSPosed 模块
- 已 Root 的 Android 手机、LSPosed、微信
- 微信 Android `8.0.75`

### 2. 启动服务端

本地内存配置示例：

```powershell
$env:BRIDGE_HTTP_ADDR=":8088"
$env:BRIDGE_ADMIN_PASSWORD="change-this-password"
$env:BRIDGE_DEFAULT_DEVICE="phone-a"
$env:BRIDGE_DEVICES="phone-a||wechat-phone|5s"
$env:BRIDGE_API_KEYS="dev_key_001|phone-a|Test Phone"
$env:BRIDGE_MYSQL_DSN=""
go run ./cmd/bridge
```

打开管理台：

```text
http://127.0.0.1:8088/admin/
```

生产环境请务必设置强密码：

```text
BRIDGE_ADMIN_PASSWORD=your-strong-admin-password
```

### 3. 使用 MySQL

```powershell
$env:BRIDGE_MYSQL_DSN="wechat:change-me@tcp(127.0.0.1:3306)/app_wechat_observatory?charset=utf8mb4&parseTime=True&loc=Local"
$env:BRIDGE_MYSQL_AUTO_MIGRATE="false"
go run ./cmd/bridge-db -create-database -migrate -seed -check
go run ./cmd/bridge
```

长运行服务建议保持：

```text
BRIDGE_MYSQL_AUTO_MIGRATE=false
```

迁移通过 `cmd/bridge-db` 显式执行，避免重启服务时误覆盖真实手机注册出来的当前 `wxid`。

### 4. 构建 Web 管理台

```powershell
cd web/admin
npm install
npm run build
```

构建产物会写入 `internal/bridge/admin_dist`，由 Go 服务直接嵌入。

### 5. 构建 Android 模块

当前仓库没有强制要求提交 Gradle Wrapper。推荐在发布前补充 Wrapper；没有 Wrapper 时，可用 Android Studio 打开 `android-module` 并执行 debug 构建。

如果仓库已包含 Gradle Wrapper：

```powershell
cd android-module
.\gradlew.bat :app:assembleDebug
```

安装 APK 后，在 LSPosed 中启用模块并勾选微信作用域，然后重启手机或重启微信。

### 6. 手机模块配置

在 Web 管理台生成 API Key，打开手机上的 **WeChat Observatory** 配置页，填写：

- 服务端地址，例如 `http://192.168.1.10:8088`
- Web 管理台生成的 API Key

不要手动填写 `wxid`。模块会在微信进程内自动识别当前登录微信。

修改配置后重启微信。模块注册成功后，Web 管理台的模块状态应显示为“就绪”。

## Docker 部署

```bash
cp deploy/docker/.env.example deploy/docker/.env
# 编辑 deploy/docker/.env，至少修改 BRIDGE_ADMIN_PASSWORD、MYSQL_PASSWORD 和 MYSQL_ROOT_PASSWORD
docker compose -f deploy/docker/docker-compose.yml up -d --build
curl -fsS http://127.0.0.1:8088/healthz
```

更多说明见 [docs/deployment.md](docs/deployment.md)。

## 常用 API

管理 API 使用 `X-Bridge-Password`：

```bash
curl -H "X-Bridge-Password: your-admin-password" http://127.0.0.1:8088/api/modules/status
curl -H "X-Bridge-Password: your-admin-password" http://127.0.0.1:8088/api/api-keys
```

模块 API 使用 API Key：

- `POST /module/register`
- `POST /webhook/lsposed/message`
- `POST /module/contacts/snapshot`
- `GET /module/outbox/ws`
- `POST /module/outbox/poll`
- `POST /module/outbox/ack`

完整协议见 [docs/api.md](docs/api.md) 和 [docs/module-contract.md](docs/module-contract.md)。

## 安全和合规提醒

本项目需要 Root、LSPosed 和对微信进程的 Hook。使用前请确认你理解并接受以下风险：

- 可能违反微信或相关服务的使用条款。
- Hook、自动化发送和异常行为可能带来账号风控风险。
- 服务端管理台必须放在可信网络后，并设置强管理密码。
- API Key 等同于手机模块访问凭证，泄露后应立即停用或删除。

更多发布前检查见 [docs/security.md](docs/security.md) 和 [docs/open-source-checklist.md](docs/open-source-checklist.md)。

## 验证

```powershell
go test ./...
cd web/admin
npm run build
cd ../..
cd android-module
.\gradlew.bat :app:assembleDebug
```

如果当前检出没有 `gradlew.bat`，请用 Android Studio 打开 `android-module` 构建，或先为项目补充 Gradle Wrapper。

## 文档

- [快速开始](docs/quick-start.md)
- [部署指南](docs/deployment.md)
- [Android 模块使用](docs/android-module.md)
- [API 说明](docs/api.md)
- [架构说明](docs/architecture.md)
- [模块协议](docs/module-contract.md)
- [安全说明](docs/security.md)
- [开源发布清单](docs/open-source-checklist.md)

## 许可证

本项目使用 MIT License。你可以自由使用、复制、修改、分发、商用或私有使用，只需要保留许可证和免责声明。
