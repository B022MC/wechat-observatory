# Android Module

Android 模块是 `wechat-observatory` 的微信进程适配层。它只负责微信 I/O：

- 自动识别当前微信 `wxid`。
- 注册当前微信通道。
- 观察收发消息。
- 同步联系人。
- 上传可读取的媒体附件。
- 接收服务端出站队列并在微信内发送文本。

模块不包含游戏业务、命令解析、自动回复或第三方协议登录。

当前公开适配目标是微信 Android `8.0.75`。微信内部类和数据库细节会随版本变化，升级微信后需要重新验证消息观察、联系人同步、媒体上传和发送路径。

## 构建

没有 Gradle Wrapper 时，使用 Android Studio 打开 `android-module` 并构建 debug APK。若仓库已经补充 Wrapper，可使用：

```powershell
cd android-module
.\gradlew.bat :app:assembleDebug
```

安装 debug APK 后，在 LSPosed 中启用模块并勾选微信作用域。

## 手机端配置

打开手机上的 **WeChat Observatory**。服务端已固定为正式环境，只需填写：

- API Key：从 Web 管理台生成
- 轮询间隔：默认 `1000`
- 通讯录同步间隔：默认 `600000`
- 是否包含群聊：默认 `1`
- 是否上传媒体：默认 `0`

模块不会要求用户填写 `wxid`。切换微信账号后，模块会重新识别当前账号并用同一个 API Key 更新服务端绑定。

服务端已固定为正式环境 `https://47.108.171.42/observatory`，设置页不提供地址输入。API Key 始终以掩码显示，界面不提供明文查看入口；可以直接覆盖输入新值，保存不会清除未修改的已有值。保存配置后需要重启微信。

## 配置项

| Key | 默认值 | 说明 |
| --- | --- | --- |
| `enabled` | `1` | 是否启用模块逻辑 |
| `bridge_url` | 不适用 | 旧配置项，正式版模块会忽略该值 |
| `api_key` | 空 | Web 管理台生成的 API Key |
| `poll_interval_ms` | `1000` | HTTP 轮询出站消息间隔 |
| `poll_limit` | `1` | 每次最多拉取条数，服务端当前只租约一条 |
| `contact_sync_interval_ms` | `600000` | 通讯录同步间隔，`0` 表示关闭 |
| `contact_sync_limit` | `10000` | 一次同步联系人数量上限 |
| `contact_include_chatrooms` | `1` | 是否同步群聊 |
| `media_upload_enabled` | `0` | 是否上传图片、语音、视频、文件等附件 |
| `media_upload_limit_bytes` | `5242880` | 单个附件上传上限 |

## 注册流程

模块在微信进程可用后调用：

```text
POST /module/register
```

请求里包含 `api_key` 和模块识别到的当前微信 `wxid`。服务端会返回 Web 管理台绑定的设备名。

如果 API Key 被删除或停用，模块请求会失败，Web 管理台会显示未注册或不可用状态。

## 发送流程

1. 管理台调用 `POST /api/send/text` 创建出站任务。
2. 服务端通过 WebSocket 唤醒模块。
3. 模块在微信进程里执行发送。
4. 模块通过 WebSocket 或 HTTP ACK 回报结果。

WebSocket 不可用时，模块会退回 HTTP 轮询。

## 联系人和媒体

联系人快照上传到：

```text
POST /module/contacts/snapshot
```

消息上传到：

```text
POST /webhook/lsposed/message
```

图片、语音、视频、文件等附件如果能从微信本地文件中读取，会以 `media_base64` 上传。服务端保存后返回管理台可访问的 `media_url`。

如果模块只能识别消息类型但读不到附件文件，管理台会显示 `[图片]`、`[语音]` 等占位文本。

## 排查

- 模块未注册：检查 API Key 是否存在、是否停用、手机是否能访问服务端。
- Web 没有好友：等待通讯录同步，或检查 `contact_sync_interval_ms`、LSPosed 作用域。
- 收不到群消息：确认模块启用、微信已重启、群聊消息进入微信数据库。
- 发消息卡在待发送：确认当前设备对应的 API Key 已注册，当前微信账号没有切换到旧绑定。
- 配置无效：保存后重启微信；必要时重启手机。
