# WeChat account switching

Version 0.1.8-account-sessions (Android versionCode 9) separates a physical device,
its current WeChat owner, and each runtime login. Contacts continue to use
`(device, owner_wxid, wxid)`: two owners can have the same peer with different
remarks, deletion states and histories.

## Protocol

The main WeChat process stores an installation UUID and increasing generation in
private `observatory_account_sessions` SharedPreferences. Each runtime binding
gets a random session. The generation is synchronously committed before any
registration; process restart, account change, or endpoint/Key change creates a
new session. Repeated kernel identity checks reuse the binding. Identity comes
from the verified current kernel storage, not contact counts, nicknames or an
old database. The insert callback captures its binding before asynchronous work.

Registration adds `instance_id`, `account_session`, and `account_generation`.
The server must echo the accepted session and generation; missing/wrong echoes
leave this module pending. Snapshot, HTTP poll/ACK and WebSocket requests carry
`account_session`. Observed messages also carry their fixed `owner_wxid`.

Two additive MySQL tables keep the current binding and immutable session history:
`bridge_module_account_current`, `bridge_module_account_history`. Sessions bind
the installation, generation, owner and credential reference/version. History is
retained as identity evidence, independently from 15-day message retention.

| Request | Rule |
| --- | --- |
| Registration retry | Identical session/binding is idempotent; does not cancel queued work |
| New registration | Same installation and strictly higher generation |
| Another installation using the same device | 409; give it a separate device/Key binding |
| Lower generation / reused session with changed identity | 409 |
| Legacy request on a protected device | 409; no fallback to the latest owner |
| Message from an old verified session | Store under its original owner; self endpoint must match |
| Snapshot / poll / ACK | Require the current session, including after A → B → A |
| Revoked/rebound credential | Historical sessions with the old auth version are rejected |

Each device's registration, ingestion, snapshot, leasing, ACK and send enqueue
share a MySQL named lock across server replicas. Nested SQL uses the same reserved
connection, including when the pool has only one connection. Registration updates
current/history bindings, device identity, runtime activity and cancellation of
all previous `pending`/`leased` outbox rows in one transaction. Terminal cancelled
rows never become eligible again. Already submitted WeChat network operations
can still complete in their original account; cancellation is not a recall.

Session tokens are removed before event persistence/publication. Event keys retain
the owner and exclude transport sessions, so identical retries across reconnects
keep the same key. The module retries message persistence errors before advancing
its ordered message watermark. It does not backfill old chat history at startup.

## Browser and other callers

Module status includes `account_generation` (0 for legacy bindings). The built-in
admin sends that generation with `owner_wxid`; the server checks it under the
device lock and returns 409 for delayed sends after an account round trip. The
page clears contacts/messages/selection/drafts/dialogs on scope changes, rejects
old response epochs and out-of-order queries, filters rows and SSE by owner, and
refreshes module status even when no chat is selected.

For compatibility, existing trusted server integrations may omit
`account_generation` and keep the existing current-owner-at-enqueue semantics.
They receive cancellation of old queued work, but cannot detect a delayed request
created before A → B → A unless they carry the generation captured when that work
was created. New manual-send clients should always supply it, including 0 for
legacy status. This change does not modify Gateway business workflows.

## Rollout and recovery

1. Back up Observatory. Run the new migration binary against its database as a
   one-shot step; do not migrate Gateway's database.
2. Replace all Observatory replicas with the session-aware server and browser
   assets before upgrading phones. Old replicas do not enforce this protocol.
3. Install the versionCode 9 APK, retain module/WeChat data, enable the existing
   LSPosed scope and restart WeChat. An old server leaves this module pending.
4. Verify actual wxid, session echo, fresh contact snapshot and an A → B → A
   switch. Check a shared contact under both owners. JVM/SQL/browser tests do
   not replace this phone verification.

Existing legacy phones can run on the new server until their first protected
registration. After that, downgrading their module to a sessionless version is
rejected. Roll back using a session-aware release; do not erase binding history
to admit stale clients. Reinstalling/clearing WeChat data creates a new installation
and should use a new device/Key binding after checking the old installation.
After a credential auth-version change, restart the module process to allocate a
new generation before resuming. A second installation should never share a binding.

The verified kernel bindings cover WeChat 8.0.74 and 8.0.78 APK definitions;
8.0.76 is still pending verification. Production and connected-phone verification
are separate from the local test results.

This prevents new cross-account attribution; it does not guess or rewrite past
owners. The 61538E backup-scoped dry-run recovery remains separate. The 16 audited
61538C conflict candidates require source verification and consistent event-key
handling before any historical repair.

## Reproducible checks

- Go: `go test ./...`, `go test -race ./internal/bridge ./internal/storage/mysql`,
  `go vet ./...`.
- Set `WECHAT_OBSERVATORY_MYSQL_TEST_DSN` to a disposable MySQL 8.4 database whose
  name contains `test` to run the integration tests. Never use production here.
  The account suite exercises two service instances with one connection per pool,
  restart durability, shared peers, rollback, concurrent generations and revocation.
- Admin (Node 24): `npm ci`, `npm test`, `npx playwright install chromium`,
  `npm run test:ui`, `npm run build`. Desktop/mobile tests mock API sends locally.
- Android: `:app:testDebugUnitTest :app:assembleDebug` with the Android 35 SDK.
- Build the Linux amd64 image from the repository Dockerfile.
