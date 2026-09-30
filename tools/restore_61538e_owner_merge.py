#!/usr/bin/env python3
"""Reverse only the verified 2026-09-19 03:06:02 owner merge; dry-run by default.

Connection: OBS_DB_HOST, OBS_DB_USER, OBS_DB_PASSWORD, OBS_DB_NAME; optional
OBS_DB_PORT. MySQL session time zone is +08:00, matching the incident backup.
Apply requires --apply --expected-plan SHA256 --recovery-backup NEW_JSON_PATH.
Stop the old module before applying; install/verify current-account identity
before collecting again. This tool never assigns a hash to a guessed real ID.
"""

import argparse
from collections import Counter
from datetime import date, datetime
import hashlib
import json
import os
from pathlib import Path

DEVICE = "61538E"
MERGED_OWNER = "acct_ee1da3ae2100e09165c2e52382cfe79f"
MERGE_TIME = "2026-09-19 03:06:02"
CONTACTS = "bridge_module_contacts"
EVENTS = "bridge_message_events"
BACKUP_CONTACTS = "bak_61538E_contacts_20260919"
BACKUP_EVENTS = "bak_61538E_events_20260919"
CONTACT_OWNERS = {"a565265501": 90, "acct_a928b1f6051e2ea0bc7bc5decd5f05ed": 90}
EVENT_OWNERS = {"a565265501": 11, "acct_5fb30db07a876e324c807f07716e7909": 1,
                MERGED_OWNER: 23}


def normalize(value):
    if isinstance(value, (datetime, date)):
        return str(value)
    if isinstance(value, dict):
        return {k: normalize(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [normalize(v) for v in value]
    return value


def canonical(value):
    return json.dumps(normalize(value), sort_keys=True, ensure_ascii=False, separators=(",", ":"))


def require(condition, reason):
    if not condition:
        raise ValueError(reason)


def indexed(rows):
    result = {row["id"]: row for row in rows}
    require(len(result) == len(rows), "Duplicate row IDs")
    require(all(row["device"] == DEVICE for row in rows), "Device mismatch / reused backup ID")
    return result


def plan(snapshot):
    """Pure preflight: require the exact merge result or an already restored state."""
    data = normalize(snapshot)
    bc, be, current, events = [indexed(data[key]) for key in
                               ("backup_contacts", "backup_events", "contacts", "events")]
    require(dict(Counter(r["owner_wxid"] for r in bc.values())) == CONTACT_OWNERS,
            "Unexpected contact backup owners/counts")
    require(dict(Counter(r["owner_wxid"] for r in be.values())) == EVENT_OWNERS,
            "Unexpected event backup owners/counts")
    require(set(be) <= set(events), "A backed-up message is missing")
    scoped_events = {key: events[key] for key in be}
    restored = current == bc and scoped_events == be
    if not restored:
        winners = {}
        for row in bc.values():
            key = row["wxid"]
            rank = lambda r: (r.get("last_seen_at") or "", r.get("updated_at") or "", r["id"])
            if key not in winners or rank(row) > rank(winners[key]):
                winners[key] = row
        expected = {r["id"]: dict(r, owner_wxid=MERGED_OWNER, updated_at=MERGE_TIME)
                    for r in winners.values()}
        require(len(expected) == 91, "Unexpected deduplication result")
        require(current == expected,
                "Contacts diverged from the exact merge result; new snapshots/edits need a new review")
        for key, original in be.items():
            require(events[key] == dict(original, owner_wxid=MERGED_OWNER),
                    "Backed-up event differs beyond the original owner merge: ID %s" % key)
    digest_data = {"backup_contacts": bc, "backup_events": be,
                   "contacts": current, "scoped_events": scoped_events}
    return {
        "device": DEVICE, "state": "already_restored" if restored else "ready",
        "plan_sha256": hashlib.sha256(canonical(digest_data).encode()).hexdigest(),
        "contact_rows_before": len(current), "contact_rows_after": len(bc),
        "deleted_contact_rows_restored": sum(r["is_deleted"] == 1 for r in bc.values()),
        "event_owner_updates": sum(events[key]["owner_wxid"] != row["owner_wxid"]
                                   for key, row in be.items()),
        "new_events_preserved": len(events) - len(be),
        "restored_contact_owners": CONTACT_OWNERS, "backed_up_event_owners": EVENT_OWNERS,
    }


def read_snapshot(cursor, lock=False):
    suffix = " FOR UPDATE" if lock else ""
    data = {}
    for key, table in (("backup_contacts", BACKUP_CONTACTS), ("backup_events", BACKUP_EVENTS)):
        cursor.execute("SELECT * FROM `%s`%s" % (table, suffix))
        data[key] = list(cursor.fetchall())
    ids = [r["id"] for r in data["backup_contacts"]]
    require(ids, "Empty contact backup")
    placeholders = ",".join(["%s"] * len(ids))
    # Lock the device partition and any colliding IDs, including its insertion gaps.
    cursor.execute("SELECT * FROM `%s` WHERE device=%%s OR id IN (%s)%s" %
                   (CONTACTS, placeholders, suffix), [DEVICE] + ids)
    data["contacts"] = list(cursor.fetchall())
    event_ids = [r["id"] for r in data["backup_events"]]
    require(event_ids, "Empty event backup")
    event_placeholders = ",".join(["%s"] * len(event_ids))
    cursor.execute("SELECT * FROM `%s` WHERE id IN (%s)%s" % (EVENTS, event_placeholders, suffix),
                   event_ids)
    scoped = list(cursor.fetchall())
    cursor.execute("SELECT * FROM `%s` WHERE device=%%s AND id NOT IN (%s)" %
                   (EVENTS, event_placeholders), [DEVICE] + event_ids)
    data["events"] = scoped + list(cursor.fetchall())
    return data


def apply_changes(cursor, data):
    """Preconditions and locks belong to the caller's single transaction."""
    before = plan(data)
    if before["state"] == "already_restored":
        return
    ids = [r["id"] for r in data["contacts"]]
    cursor.execute("DELETE FROM `%s` WHERE device=%%s AND id IN (%s)" %
                   (CONTACTS, ",".join(["%s"] * len(ids))), [DEVICE] + ids)
    require(cursor.rowcount == len(ids), "Contact delete count changed")
    columns = list(data["backup_contacts"][0])
    require(all(col.replace("_", "").isalnum() for col in columns), "Invalid column")
    cursor.executemany("INSERT INTO `%s` (%s) VALUES (%s)" %
                       (CONTACTS, ",".join("`%s`" % col for col in columns),
                        ",".join(["%s"] * len(columns))),
                       [[row[col] for col in columns] for row in data["backup_contacts"]])
    original_events = {row["id"]: row for row in data["backup_events"]}
    for row in data["events"]:
        original = original_events.get(row["id"])
        if original and row["owner_wxid"] != original["owner_wxid"]:
            cursor.execute("UPDATE `%s` SET owner_wxid=%%s WHERE id=%%s AND device=%%s "
                           "AND owner_wxid=%%s" % EVENTS,
                           [original["owner_wxid"], row["id"], DEVICE, MERGED_OWNER])
            require(cursor.rowcount == 1, "Event owner changed during recovery")


def run(connection, apply=False, expected_plan=None, recovery_backup=None):
    try:
        with connection.cursor() as cursor:
            cursor.execute("SET SESSION time_zone = '+08:00'")
            cursor.execute("SET SESSION innodb_lock_wait_timeout = 10")
            cursor.execute("SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ")
            cursor.execute("SET TRANSACTION READ WRITE" if apply else "SET TRANSACTION READ ONLY")
            cursor.execute("START TRANSACTION")
            data = read_snapshot(cursor, lock=apply)
            preview = plan(data)
            if not apply:
                connection.rollback()
                return dict(preview, mode="dry_run")
            require(expected_plan == preview["plan_sha256"], "Preview fingerprint changed; run dry-run again")
            if preview["state"] == "already_restored":
                connection.rollback()
                return dict(preview, mode="no_op")
            require(recovery_backup, "A new recovery backup file is required")
            # Exclusive creation and fsync complete before the first data mutation.
            with Path(recovery_backup).open("x", encoding="utf-8") as handle:
                json.dump({"preview": preview, "snapshot": normalize(data)}, handle,
                          ensure_ascii=False, indent=2)
                handle.flush()
                os.fsync(handle.fileno())
            apply_changes(cursor, data)
            result = plan(read_snapshot(cursor, lock=True))
            require(result["state"] == "already_restored", "Recovery postcondition failed")
            connection.commit()
            return dict(result, mode="applied", recovery_backup=str(recovery_backup))
    except BaseException:
        connection.rollback()
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--expected-plan")
    parser.add_argument("--recovery-backup", type=Path)
    parser.add_argument("--snapshot", type=Path, help="Validate a local JSON snapshot, without DB access")
    args = parser.parse_args()
    if args.snapshot:
        require(not args.apply, "Local snapshot mode is read-only")
        print(json.dumps(dict(plan(json.loads(args.snapshot.read_text(encoding="utf-8"))),
                              mode="local_preview"), ensure_ascii=False, indent=2))
        return
    if args.apply:
        require(args.expected_plan and args.recovery_backup, "Apply requires preview hash and backup path")
    import pymysql
    connection = pymysql.connect(host=os.environ["OBS_DB_HOST"], user=os.environ["OBS_DB_USER"],
                                 password=os.environ["OBS_DB_PASSWORD"],
                                 database=os.environ["OBS_DB_NAME"],
                                 port=int(os.getenv("OBS_DB_PORT", "3306")), charset="utf8mb4",
                                 cursorclass=pymysql.cursors.DictCursor, autocommit=False,
                                 connect_timeout=10, read_timeout=30, write_timeout=30)
    try:
        print(json.dumps(run(connection, args.apply, args.expected_plan, args.recovery_backup),
                         ensure_ascii=False, indent=2))
    finally:
        connection.close()


if __name__ == "__main__":
    main()
