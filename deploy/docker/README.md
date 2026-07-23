# PD WeChat Runtime Docker Deployment

This Compose project starts one MySQL server, the WeChat Observatory migration
job and bridge, then the PD Gateway migration job and gateway. It is the
initial single-host deployment: Gateway uses `standalone/all`, so exactly one
process can own the configured game account.

## Start

1. Copy `.env.example` to `.env` and replace every placeholder secret.
2. Keep `PD_GATEWAY_GAME_RUNTIME_ENABLED=false` until the previous Plaza
   account owner has been stopped and the tea-house binding is verified.
3. Run `docker compose up -d --build` from this directory.
4. Check `http://host:8088/healthz` and `http://host:19090/healthz`.

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
