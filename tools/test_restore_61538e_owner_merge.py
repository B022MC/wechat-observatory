import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

import restore_61538e_owner_merge as repair


def fixture():
    contacts = []
    for batch, owner in enumerate(repair.CONTACT_OWNERS):
        for number in range(90):
            contacts.append(dict(id=batch * 90 + number + 1, device=repair.DEVICE,
                                 owner_wxid=owner,
                                 wxid="peer_%s" % number if number < 89 else "unique_%s" % batch,
                                 nickname="name", is_deleted=int(batch == 0 and number == 89),
                                 last_seen_at="2026-09-19 00:%s:00" % ("41" if batch else "30"),
                                 updated_at="2026-09-19 00:%s:00" % ("41" if batch else "30")))
    originals = []
    for owner, count in repair.EVENT_OWNERS.items():
        for _ in range(count):
            originals.append(dict(id=len(originals) + 1, device=repair.DEVICE,
                                  owner_wxid=owner, text="unchanged message", chat_record_id=len(originals)))
    retained = [contacts[89]] + contacts[90:]
    return dict(backup_contacts=contacts, backup_events=originals,
                contacts=[dict(r, owner_wxid=repair.MERGED_OWNER, updated_at=repair.MERGE_TIME)
                          for r in retained],
                events=[dict(r, owner_wxid=repair.MERGED_OWNER) for r in originals]
                       + [dict(id=100, device=repair.DEVICE, owner_wxid=repair.MERGED_OWNER,
                               text="new message", chat_record_id=100)])


class Cursor:
    """SQLite executes DML; only MySQL transaction syntax is adapted for fixtures."""
    def __init__(self, connection):
        self.connection = connection
        self.raw = connection.db.cursor()

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.raw.close()

    @property
    def rowcount(self):
        return self.raw.rowcount

    def execute(self, sql, parameters=()):
        if sql.startswith("SET "):
            return
        if sql.startswith("START TRANSACTION"):
            return self.raw.execute("BEGIN")
        if self.connection.fail_update and sql.startswith("UPDATE "):
            raise RuntimeError("injected database failure")
        return self.raw.execute(sql.replace(" FOR UPDATE", "").replace("%s", "?"), parameters)

    def executemany(self, sql, parameters):
        return self.raw.executemany(sql.replace("%s", "?"), parameters)

    def fetchall(self):
        return [dict(r) for r in self.raw.fetchall()]


class Connection:
    def __init__(self, snapshot):
        self.db = sqlite3.connect(":memory:")
        self.db.row_factory = sqlite3.Row
        self.fail_update = False
        for key, table in (("backup_contacts", repair.BACKUP_CONTACTS), ("contacts", repair.CONTACTS),
                           ("backup_events", repair.BACKUP_EVENTS), ("events", repair.EVENTS)):
            columns = list(snapshot[key][0])
            declarations = ["`%s` %s" % (col, "INTEGER" if isinstance(snapshot[key][0][col], int)
                                        else "TEXT") for col in columns]
            self.db.execute("CREATE TABLE `%s` (%s, PRIMARY KEY(id))" % (table, ",".join(declarations)))
            self.db.executemany("INSERT INTO `%s` VALUES (%s)" % (table, ",".join("?" for _ in columns)),
                                [[r[col] for col in columns] for r in snapshot[key]])
        self.db.commit()

    def cursor(self):
        return Cursor(self)

    def commit(self):
        self.db.commit()

    def rollback(self):
        self.db.rollback()


class RecoveryTest(unittest.TestCase):
    def test_preview_identifies_only_original_merge(self):
        result = repair.plan(fixture())
        self.assertEqual((91, 180, 12, 1), tuple(result[k] for k in
                         ("contact_rows_before", "contact_rows_after", "event_owner_updates",
                          "new_events_preserved")))
        self.assertEqual(1, result["deleted_contact_rows_restored"])

    def test_divergence_is_rejected(self):
        mutations = [lambda d: d["contacts"][0].update(nickname="new nickname"),
                     lambda d: d["contacts"][0].update(updated_at="2026-09-19 04:00:00"),
                     lambda d: d["contacts"].pop(),
                     lambda d: d["events"][0].update(text="changed"),
                     lambda d: d["events"][0].update(device="other"),
                     lambda d: d["backup_events"].pop()]
        for mutate in mutations:
            data = fixture()
            mutate(data)
            with self.subTest(mutation=mutate), self.assertRaises(ValueError):
                repair.plan(data)

    def test_new_messages_do_not_change_reviewed_plan(self):
        data = fixture()
        before = repair.plan(data)
        data["events"].append(dict(data["events"][-1], id=101))
        after = repair.plan(data)
        self.assertEqual(before["plan_sha256"], after["plan_sha256"])
        self.assertEqual(2, after["new_events_preserved"])

    def test_dry_run_leaves_all_rows_unchanged(self):
        data = fixture()
        connection = Connection(data)
        result = repair.run(connection)
        self.assertEqual("dry_run", result["mode"])
        self.assertEqual(data, repair.read_snapshot(connection.cursor()))

    def test_apply_restores_backup_preserves_new_messages_and_is_idempotent(self):
        data = fixture()
        connection = Connection(data)
        with tempfile.TemporaryDirectory() as directory:
            backup = Path(directory) / "recovery.json"
            result = repair.run(connection, True, repair.plan(data)["plan_sha256"], backup)
            self.assertEqual("applied", result["mode"])
            saved = json.loads(backup.read_text(encoding="utf-8"))
            self.assertEqual(data, saved["snapshot"])
            restored = repair.read_snapshot(connection.cursor())
            self.assertEqual(data["backup_contacts"], restored["contacts"])
            self.assertEqual(data["backup_events"] + [data["events"][-1]], restored["events"])
            self.assertEqual("no_op", repair.run(connection, True, result["plan_sha256"], backup)["mode"])

    def test_failure_after_contact_writes_rolls_everything_back(self):
        data = fixture()
        connection = Connection(data)
        connection.fail_update = True
        with tempfile.TemporaryDirectory() as directory:
            backup = Path(directory) / "recovery.json"
            with self.assertRaises(RuntimeError):
                repair.run(connection, True, repair.plan(data)["plan_sha256"], backup)
            self.assertEqual(data, repair.read_snapshot(connection.cursor()))
            self.assertTrue(backup.exists())

    def test_wrong_fingerprint_and_existing_backup_abort_before_writing(self):
        data = fixture()
        connection = Connection(data)
        with tempfile.TemporaryDirectory() as directory:
            backup = Path(directory) / "recovery.json"
            with self.assertRaises(ValueError):
                repair.run(connection, True, "wrong", backup)
            self.assertFalse(backup.exists())
            backup.write_text("existing", encoding="utf-8")
            with self.assertRaises(FileExistsError):
                repair.run(connection, True, repair.plan(data)["plan_sha256"], backup)
            self.assertEqual(data, repair.read_snapshot(connection.cursor()))


if __name__ == "__main__":
    unittest.main()
