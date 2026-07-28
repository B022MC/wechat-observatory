# PD WeChat Runtime Docker Deployment

This Compose project starts one MySQL server, the WeChat Observatory migration
job and bridge, then the PD Gateway migration job and gateway. It is the
initial single-host deployment: Gateway uses `standalone/all`, so exactly one
process can own the configured game account.

## Start

1. Copy `.env.example` to `.env` and replace every placeholder secret.
   A deliberately blank installation may set `BRIDGE_API_KEYS=` and create
   its first module API Key from the Observatory admin page after startup.
   `BRIDGE_DEVICES` must still contain one bootstrap device.
   Set `BRIDGE_DEVICE_ADMIN_PASSWORD` to a secret distinct from
   `BRIDGE_ADMIN_PASSWORD`; it grants only the message-free `/device` surface.
2. Keep `PD_GATEWAY_GAME_RUNTIME_ENABLED=false` until the previous Plaza
   account owner has been stopped and the tea-house binding is verified.
3. Run `docker compose up -d --build` from this directory.
4. Check `http://127.0.0.1:8088/healthz` and `http://127.0.0.1:19090/healthz`.

## HTTPS And Boss PWA

Set `PD_WECHAT_HTTPS_HOST` to the public IP or to a DNS name that points at the
Docker host, and set the matching `https://` value in
`PD_GATEWAY_PUBLIC_BASE_URL`. The included Caddy policy requests automatically
renewed Let's Encrypt short-lived certificates, which also support public IP
addresses. Then generate a dedicated Boss Key pepper:

```bash
openssl rand -hex 32
```

Store the result only as `PD_GATEWAY_BOSS_KEY_PEPPER` in `.env`. Enable the
Boss app with `PD_GATEWAY_BOSS_APP_ENABLED=true`, keep
`PD_GATEWAY_BOSS_COOKIE_SECURE=true`, and start the HTTPS proxy after the base
stack has created `pd-wechat-runtime_default`:

```bash
docker compose -p pd-wechat-https -f docker-compose.https.yml up -d
```

Caddy runs as a separate Compose project while joining the base stack through
`PD_WECHAT_RUNTIME_NETWORK`. Keep its default unless the base stack uses a
different explicit network name. The public phone module URL is
`https://<host>/observatory`; Caddy strips `/observatory` and forwards the
module API and WebSocket upgrade to `observatory:8088`.

Caddy persists ACME certificates in named volumes and proxies the complete
Gateway origin, including `/admin/`, `/boss/`, `/reports/`, and both API
namespaces. The Gateway and Observatory host ports bind to `127.0.0.1`; do not
reopen 19090 or 8088 to the Internet after HTTPS is verified.

MySQL stores two isolated application databases. The Compose MySQL bootstrap
uses root only once to create the users; Gateway and Observatory use their own
DSNs and must never use the root account. Gateway receives only the additional
read-only `REPLICATION CLIENT` privilege needed to total MySQL binlog sizes; it
cannot read the Observatory database. No media data is mounted or stored.

Observatory deletes message history and only successfully sent outbox rows
after 15 days. Gateway deletes processed command receipts and command logs
after 30 days. Pending, leased, and failed Observatory outbox rows remain;
financial, diamond, balance, and game-settlement business ledgers are not
removed by these jobs.

Gateway checks combined application database size, MySQL binlog size, node
disk usage, and node/container memory every five minutes. The defaults alert
at 5 GiB, 4 GiB, 85%, and 85%. Each enabled Boss receives the warning through
that Boss's own assigned device and `filehelper`, at most once per six hours.
For an existing MySQL volume, apply the binlog monitoring grant once before
starting the updated Gateway:

```sql
GRANT REPLICATION CLIENT ON *.* TO '<gateway-db-user>'@'%';
```

## Rollback

Scale or stop `gateway` before restoring any previous Plaza owner. Do not run
two processes with the same tea-house game login. Database migration is
additive; preserve `mysql-data` unless a verified backup restoration is
required.

## K3s Cutover And Failover Drill

1. Run the two migration Jobs, then deploy Observatory and Gateway API/control
   with `PD_GATEWAY_GAME_RUNTIME_ENABLED=false`.
   The external MySQL administrator must also grant `REPLICATION CLIENT` to
   the Gateway database user; no cross-database grant is required.
2. Stop Compose `gateway`; deploy one account-worker manifest with one explicit
   `PD_GATEWAY_RUNTIME_TEAHOUSE_ID`, then enable the intended runtime features.
3. For takeover testing, deploy a second one-replica worker manifest for the
   same tea-house, stop the current owner, and observe one successor after the
   30-second lease interval.
4. Interrupt a dispatched diamond or player-rights operation during takeover.
   Its `gateway_runtime_job` and business operation must become `unknown`; it
   must not be replayed automatically.
5. Roll back by scaling all account workers to zero before restoring the
   previous owner. API/control replicas can remain scaled because they never
   open Plaza sessions.
