# Module Contract

## Scope

`wechat-observatory` connects a phone-installed LSPosed module to a web admin
console. The module owns WeChat process I/O. The gateway owns API-key
identity, current WeChat binding, durable events, contacts, module runtime
status, outbox state, and admin APIs.

The gateway is not a business bot. It does not parse command text, call
external business services, or create automatic replies.

## Endpoints

Module endpoints use the Web-issued `api_key` only. JSON POST endpoints carry
`"api_key":"..."` in the request body. The WebSocket endpoint carries
`?api_key=...` in the query string.

- `POST /module/register`
- `POST /webhook/lsposed/message`
- `POST /webhook/module/message`
- `POST /module/contacts/snapshot`
- `POST /module/outbox/poll`
- `POST /module/outbox/ack`
- `GET /module/outbox/ws?api_key=<api_key>&device=<device>&wxid=<wxid>`

Admin endpoints use `X-Bridge-Password: <BRIDGE_ADMIN_PASSWORD>` or
`?password=...`.

- `GET /admin/`
- `GET /api/devices`
- `GET /api/api-keys`
- `GET /api/events`
- `GET /api/stored-events`
- `GET /api/messages`
- `GET /api/live/events`
- `GET /api/modules/status`
- `GET /api/module-contacts`
- `POST /api/devices`
- `POST /api/api-keys`
- `POST /api/api-keys/{key}/disable`
- `POST /api/api-keys/{key}/enable`
- `DELETE /api/api-keys/{key}`
- `POST /api/send/text`

## Registration

`POST /module/register`

Request:

```json
{
  "api_key": "wg_device_key",
  "device": "",
  "wxid": "wxid_current_login",
  "nickname": "Current WeChat Nickname"
}
```

Rules:

- API Key identity is authoritative.
- API Keys are generated and managed by the Web admin console.
- The same API Key keeps the same stable gateway identity when WeChat `wxid` changes.
- The module must detect the current WeChat `wxid` inside the WeChat process.
  Users must not type `wxid` manually in the module configuration.
- The server-chosen device name is authoritative. If the API Key is
  bound to a Web-managed device, module-provided `device` is ignored.
- Device display names are Web-managed. Module registration must not overwrite a
  display name configured from the admin console.
- Re-registering the same API Key with a new `wxid` updates the current channel
  binding and removes the old `wxid` identity mapping.
- Contact snapshots must not create API Key identities.

Admin API Key payload:

```json
{
  "api_key": "",
  "device": "phone-a",
  "nickname": "Front Desk Phone"
}
```

If `api_key` is blank, the gateway generates one. The API Key itself is the
stable module identity. The gateway does not accept legacy identity fields.

Admin device payload:

```json
{
  "name": "phone-a",
  "nickname": "Front Desk Phone"
}
```

Only known devices can be renamed from the admin console.

## Message Webhook

`POST /webhook/lsposed/message`

Core fields:

```json
{
  "api_key": "wg_device_key",
  "id": "local-source-id",
  "event_id": 123,
  "chat_record_id": 456,
  "device": "phone-a",
  "from": "wxid_peer",
  "to": "wxid_current_login",
  "room_id": "",
  "sender": "",
  "text": "hello",
  "message_type": 1,
  "direction": "recv",
  "create_time": 1710000000,
  "chat_id": "wxid_peer",
  "chat_kind": "direct"
}
```

Rules:

- `direction` is `recv` or `sent`.
- `chat_id` and `chat_kind` are preferred for new module clients.
- Direct chats use the peer `wxid` as `chat_id`.
- Group chats use the chatroom id as `chat_id` and `chat_kind=room`.
- For group chats, `sender` should be the real member `wxid` when available.
- If `device` is omitted, the default device is used.
- The gateway fills `owner_wxid` from the device's current registered WeChat
  wxid.
- The gateway publishes the event to live listeners and persists it.
- The gateway does not parse command text and does not enqueue automatic replies.

Response:

```json
{
  "ok": true,
  "result": {
    "published": true
  }
}
```

## Media

Message payloads may include:

- `media_kind`
- `media_mime`
- `media_name`
- `media_size`
- `media_base64`
- `media_url`

If `media_base64` is present, the gateway discards the raw bytes before
persistence. The current transport deployment does not create a `media_url`
or expose a media download endpoint.

If media bytes are missing, the event is still valid when `text` carries a
placeholder such as `[图片]` or `[语音]` and `media_kind` identifies the attachment.

## Contacts

`POST /module/contacts/snapshot`

Request:

```json
{
  "device": "phone-a",
  "wxid": "wxid_current_login",
  "complete": true,
  "contacts": [
    {
      "wxid": "wxid_friend",
      "nickname": "Friend",
      "remark": "Remark",
      "alias": "alias",
      "type": 3,
      "verify_flag": 0,
      "chatroom": false,
      "deleted": false
    }
  ]
}
```

Rules:

- Blank contact `wxid` rows are ignored.
- More than 10000 contacts is a validation error.
- `complete=true` marks previous rows for the same device and owner as deleted
  before upserting the uploaded contacts.
- Modules should skip upload rather than send an empty complete snapshot when
  WeChat contacts cannot be read.
- `filehelper` should remain a visible direct conversation.

## Outbox

Manual admin sends call `POST /api/send/text` and create rows in
`bridge_module_outbox`.

Request:

```json
{
  "device": "phone-a",
  "owner_wxid": "wxid_current_login",
  "wx_ids": ["wxid_friend"],
  "text": "hello"
}
```

Response:

```json
{
  "ok": true,
  "outbox_id": 123
}
```

`outbox_id` identifies the queued delivery task. It is not a WeChat
`chat_record_id`; the latter is known only after the module sends the message
and WeChat reports the sent event or ACK.

Rules:

- `owner_wxid` is required for admin sends.
- A stale `owner_wxid` is rejected so an old browser state cannot enqueue a
  message for the wrong WeChat login.
- The gateway leases at most one outbox item per poll or WebSocket wake.
- HTTP poll/ACK and WebSocket ACK use the same state machine.
- Successful ACKs are stored as `raw_provider=module_ack` outbound events.
- Chat views exclude `raw_provider=module_ack` so ACKs do not render as duplicate
  chat bubbles.

## WebSocket Outbox

`GET /module/outbox/ws`

Server messages:

- `ready`
- `outbox`
- `ack`
- `ping`
- `error`

Client messages:

- `ack`
- `poll`
- `wake`
- `pong`

The module should prefer WebSocket outbox delivery and keep HTTP poll/ACK as a
fallback.

## Admin Console

`/admin/` is a React + shadcn admin-password page embedded from
`internal/bridge/admin_dist`.

It should:

- Use Chinese labels.
- Support light/dark theme switching.
- Show module runtime status.
- Show friend/group/contact lists from `/api/module-contacts`.
- Show recent-message conversation rows from `/api/messages`.
- Open `/api/live/events` while auto-refresh is enabled.
- Keep periodic refresh as a fallback.
- Hide external business identity fields from normal console cards.
- Avoid showing raw `wxid` values in normal rows and chat bubbles unless needed
  for debugging.
- Provide API Key management in the console. The phone module receives only the
  issued API Key.
- Provide device display-name management in the console. Phone-side config does
  not rename devices.
- Clear list/detail state while switching current device or owner to prevent
  contacts, groups, or messages from the previous WeChat login from flashing in
  the new view.

## Validation Matrix

- Unknown API Key -> module requests fail and registration must not create a
  device binding.
- API Key missing from module registration, message webhook, contacts, outbox
  poll, or outbox ACK -> `api_key is required`.
- Legacy identity payload fields -> not part of the current contract.
- Re-registering the same API Key with a new `wxid` -> current binding changes and
  old `wxid` no longer maps to that identity.
- Module register with a stale/manual device -> server returns the Web-bound
  device for that API Key.
- Module register with a new nickname -> existing Web device display name is
  preserved.
- Valid inbound text, including strings that look like old business commands ->
  persist/publish only; no outbox row and no automatic reply.
- Valid `direction=sent` observation -> persist/publish only; no business loop.
- Module poll with `limit > 1` -> leases at most one item.
- ACK `sent` with no `chat_record_id` -> mark sent; missing local id is not a
  failure.
- ACK `failed` -> persist `last_error`.
- `/api/messages` with device and no owner -> defaults to current device
  `owner_wxid`.
- `/api/messages` excludes `raw_provider=module_ack`.
## Required Checks

- `go test ./...`
- `cd web/admin && npm run build`
- Android module build when module code changes.
- Deployment validation:
  - `/healthz`
  - `/api/modules/status`
  - `/api/messages`
  - `/api/module-contacts`
  - Inbound webhook does not enqueue automatic replies.
