# Quick Start

本文档用于从零跑起 `wechat-observatory`。项目中文名是 **微信看台**。

## 1. 准备环境

- Go 1.24.2+
- Node.js 20+ 和 npm
- MySQL 8+，生产环境建议使用 MySQL
- Android Studio / Android SDK
- 已 Root 的 Android 手机、LSPosed、微信
- 微信 Android `8.0.75`

## 2. 启动服务端

复制示例配置：

```powershell
Copy-Item .env.example .env
```

编辑 `.env`，至少修改：

```text
BRIDGE_ADMIN_PASSWORD=change-this-password
BRIDGE_API_KEYS=dev_key_001|phone-a|Test Phone
```

本地调试可以先不接 MySQL：

```powershell
go run ./cmd/bridge
```

打开管理台：

```text
http://127.0.0.1:8088/admin/
```

## 3. 使用 MySQL

创建数据库、迁移表结构、写入初始设备和 API Key：

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

迁移和种子数据通过 `cmd/bridge-db` 显式执行。

## 4. 构建 Web 管理台

```powershell
cd web/admin
npm install
npm run build
cd ../..
```

构建产物会写入 `internal/bridge/admin_dist`，Go 服务会直接嵌入这些静态资源。

## 5. 构建 Android 模块

没有 Gradle Wrapper 时，直接用 Android Studio 打开 `android-module` 并构建 debug APK。若仓库已经补充 Wrapper，可使用命令行：

```powershell
cd android-module
.\gradlew.bat :app:assembleDebug
cd ..
```

APK 位于：

```text
android-module/app/build/outputs/apk/debug/
```

## 6. 安装和配置模块

1. 安装模块 APK。
2. 在 LSPosed 中启用模块。
3. 作用域选择微信 `com.tencent.mm`。
4. 重启微信或重启手机。
5. 打开手机桌面的 **WeChat Observatory**。
6. 填写服务端地址，例如 `https://47.108.171.42/observatory`。该地址不需要
   端口，模块会使用 HTTPS API 和 WSS 出站流；WSS 失败时自动回退到 HTTP
   轮询。
7. 填写 Web 管理台生成的 API Key。
8. 保存配置并重启微信。

不要手动填写 `wxid`。模块会在微信进程内自动识别当前登录账号。

## 7. 验证链路

管理台应该显示：

- 手机模块状态为已连接或就绪。
- 当前设备有当前微信账号绑定。
- 通讯录同步后可看到好友、群聊、文件传输助手。
- 收到微信消息后，消息列表和聊天框自动刷新。
- 从 Web 发送文本后，手机微信内能看到对应消息。

如果模块显示未注册，优先检查：

- API Key 是否存在。
- API Key 是否被停用。
- API Key 是否刚被删除后还在手机上继续使用。
- 手机能否访问 `bridge_url`。
- LSPosed 作用域是否包含微信。
- 修改模块配置后是否已经重启微信。
