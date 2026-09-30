# API

> Android 0.1.8 uses persistent account sessions. For protected devices add
> `instance_id`, `account_session`, `account_generation` to registration;
> carry `account_session` on all module data requests and explicit `owner_wxid`
> on messages. Admin sends should carry the generation returned by module status.
> The examples below describe the legacy baseline; see
> [account-session-isolation.md](account-session-isolation.md) before upgrading.


`wechat-observatory` 有两类 API：

- 管理 API：Web 管理台使用，认证头是 `X-Bridge-Password`。
- 模块 API：手机 LSPosed 模块使用，认证凭据是 API Key。

示例中的服务地址使用：

```text
http://127.0.0.1:8088
```

## 管理认证

```bash
curl -H "X-Bridge-Password: your-admin-password" \
  http://127.0.0.1:8088/api/modules/status
```

也支持 `?password=...`，但不建议在生产环境使用 URL 参数传密码。

## API Key 管理

列出：

```bash
curl -H "X-Bridge-Password: your-admin-password" \
  http://127.0.0.1:8088/api/api-keys
```

创建：

```bash
curl -X POST \
  -H "X-Bridge-Password: your-admin-password" \
  -H "Content-Type: application/json" \
  -d '{"api_key":"","device":"phone-a","nickname":"Front Desk Phone"}' \
  http://127.0.0.1:8088/api/api-keys
```

`api_key` 留空时，服务端自动生成。

停用：

```bash
curl -X POST \
  -H "X-Bridge-Password: your-admin-password" \
  http://127.0.0.1:8088/api/api-keys/<api-key>/disable
```

启用：

```bash
curl -X POST \
  -H "X-Bridge-Password: your-admin-password" \
  http://127.0.0.1:8088/api/api-keys/<api-key>/enable
```

删除：

```bash
curl -X DELETE \
  -H "X-Bridge-Password: your-admin-password" \
  http://127.0.0.1:8088/api/api-keys/<api-key>
```

删除 API Key 后，对应模块身份会注销。手机端继续使用旧 API Key 时不会重新注册成功。

## 设备管理

修改设备显示名：

```bash
curl -X POST \
  -H "X-Bridge-Password: your-admin-password" \
  -H "Content-Type: application/json" \
  -d '{"name":"phone-a","nickname":"Front Desk Phone"}' \
  http://127.0.0.1:8088/api/devices
```

设备名和显示名由 Web 管理端管理，手机模块不能改名。

## 消息查询

```bash
curl -H "X-Bridge-Password: your-admin-password" \
  "http://127.0.0.1:8088/api/messages?device=phone-a&limit=50"
```

常用参数：

| 参数 | 说明 |
| --- | --- |
| `device` | 设备名 |
| `owner_wxid` | 当前登录微信账号，省略时默认当前设备绑定 |
| `wxid` | 对话对象或群聊 ID |
| `limit` | 返回数量 |

## 发送文本

```bash
curl -X POST \
  -H "X-Bridge-Password: your-admin-password" \
  -H "Content-Type: application/json" \
  -d '{"device":"phone-a","owner_wxid":"wxid_current_login","wx_ids":["wxid_friend"],"text":"hello"}' \
  http://127.0.0.1:8088/api/send/text
```

`owner_wxid` 必填。它用于防止浏览器停留在旧账号状态时，把消息发到切换后的微信账号里。

## 实时事件

```bash
curl -N -H "X-Bridge-Password: your-admin-password" \
  http://127.0.0.1:8088/api/live/events
```

管理台用该接口实时刷新模块状态和消息。

## 模块注册

```bash
curl -X POST \
  -H "Content-Type: application/json" \
  -d '{"api_key":"dev_key_001","device":"","wxid":"wxid_current_login","nickname":"Current WeChat"}' \
  http://127.0.0.1:8088/module/register
```

服务端以 API Key 为准，`device` 会被 Web 绑定覆盖。

## 模块上报消息

```bash
curl -X POST \
  -H "Content-Type: application/json" \
  -d '{"api_key":"dev_key_001","from":"wxid_friend","to":"wxid_current_login","text":"hello","message_type":1,"direction":"recv","chat_id":"wxid_friend","chat_kind":"direct"}' \
  http://127.0.0.1:8088/webhook/lsposed/message
```

群聊建议带：

```json
{
  "room_id": "12345@chatroom",
  "sender": "wxid_member",
  "chat_id": "12345@chatroom",
  "chat_kind": "room"
}
```

## 模块同步联系人

```bash
curl -X POST \
  -H "Content-Type: application/json" \
  -d '{"api_key":"dev_key_001","wxid":"wxid_current_login","complete":true,"contacts":[{"wxid":"filehelper","nickname":"文件传输助手","chatroom":false}]}' \
  http://127.0.0.1:8088/module/contacts/snapshot
```

## 模块出站队列

WebSocket：

```text
GET /module/outbox/ws?api_key=<api-key>&device=<device>&wxid=<current-wxid>
```

HTTP 轮询：

```bash
curl -X POST \
  -H "Content-Type: application/json" \
  -d '{"api_key":"dev_key_001","device":"phone-a","wxid":"wxid_current_login","limit":1}' \
  http://127.0.0.1:8088/module/outbox/poll
```

ACK：

```bash
curl -X POST \
  -H "Content-Type: application/json" \
  -d '{"api_key":"dev_key_001","device":"phone-a","items":[{"id":1,"status":"sent"}]}' \
  http://127.0.0.1:8088/module/outbox/ack
```
