#!/usr/bin/env python3
import importlib.util
import json
import os
import sqlite3
import tempfile
import unittest
from unittest.mock import patch
from pathlib import Path


HERE = Path(__file__).parent
SPEC = importlib.util.spec_from_file_location("factory_intake", HERE / "factory-intake.py")
INTAKE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(INTAKE)


def issue(state="OPEN", updated="2026-09-11T10:00:00Z", body="Do the work", labels=None):
    return {"number": 7, "title": "Improve queue", "body": body, "author": {"login": "maintainer"}, "labels": [{"name": name} for name in labels or ["factory:ready"]], "state": state, "updatedAt": updated, "url": "https://github.com/o/r/issues/7"}


class IntakeTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        home = root / "home"
        home.mkdir()
        self.config = {"repository": "o/r", "project_id": "1" * 32, "overseer_agent_id": "3" * 32, "label": "factory:ready", "allowed_authors": ["maintainer"], "factory_home": str(home), "journal": str(root / "journal.json"), "max_issues": 25, "poll_seconds": 5}
        self.source, self.calls, self.states = issue(), [], {}
        self.real_command, self.real_state = INTAKE.command, INTAKE.task_state
        INTAKE.command, INTAKE.task_state = self.command, lambda _config, operation: self.states.get(operation["task_id"])

    def tearDown(self):
        INTAKE.command, INTAKE.task_state = self.real_command, self.real_state
        self.temp.cleanup()

    def command(self, argv, **_kwargs):
        self.calls.append(argv)
        if argv[:3] == ["gh", "issue", "list"]:
            return json.dumps([{ "number": 7 }] if self.source["state"] == "OPEN" and "factory:ready" in [label["name"] for label in self.source["labels"]] else [])
        if argv[:3] == ["gh", "issue", "view"]:
            return json.dumps(self.source)
        if argv == ["factoryctl", "status"]:
            with sqlite3.connect(Path(self.config["factory_home"]) / "factory.sqlite3") as database:
                project = database.execute("SELECT id, run_budget_limit, runs_used, max_run_seconds FROM projects").fetchone()
                agent = database.execute("SELECT id, project_id, role, provider FROM agents").fetchone()
            return json.dumps({"projects": [{"id": project[0].hex(), "run_budget_limit": project[1], "runs_used": project[2], "max_run_seconds": project[3]}], "agents": [{"id": agent[0].hex(), "project_id": agent[1].hex(), "role": agent[2], "provider": agent[3]}]})
        if argv[:3] == ["factoryctl", "task", "recovery"]:
            task_id = argv[argv.index("--task") + 1]
            incarnation = argv[argv.index("--incarnation") + 1]
            state = self.states.get(task_id)
            if state is None:
                return json.dumps({"state": "missing"})
            return json.dumps({"state": "found", "task_id": task_id, "incarnation_id": incarnation, "project_id": self.config["project_id"], "assigned_agent_id": self.config["overseer_agent_id"], "status": state["status"], "needs_operator_recovery": state.get("needs_operator_recovery", False)})
        task_id, incarnation = argv[argv.index("--task-id") + 1], argv[argv.index("--incarnation-id") + 1]
        self.states[task_id] = {"status": "queued", "id": task_id, "incarnation": incarnation}
        return json.dumps({"id": task_id, "incarnation_id": incarnation})

    def test_explicit_cutover_receipt_fences_legacy_before_remote_reads_or_enqueue(self):
        marker = Path(str(Path(self.config['factory_home']).resolve()) + '.intake/migration.json')
        marker.parent.mkdir()
        marker.write_text('{}')
        with self.assertRaisesRegex(INTAKE.IntakeError, 'explicitly cut over'):
            INTAKE.run_once(self.config)
        self.assertEqual([], self.calls)

    def test_unlimited_allowance_preserves_duration_and_finite_exhaustion_checks(self):
        with sqlite3.connect(Path(self.config["factory_home"]) / "factory.sqlite3") as database:
            database.executescript("CREATE TABLE projects (id BLOB, run_budget_limit INTEGER, runs_used INTEGER, max_run_seconds INTEGER); CREATE TABLE agents (id BLOB, project_id BLOB, role TEXT, provider TEXT);")
            project = bytes.fromhex(self.config["project_id"])
            database.execute("INSERT INTO projects VALUES (?, 0, 999, 2700)", (project,))
            database.execute("INSERT INTO agents VALUES (?, ?, 'orchestrator', 'codex')", (bytes.fromhex(self.config["overseer_agent_id"]), project))
            database.commit()
            for limit, used, duration, error in [(0, 999, 2700, None), (2, 1, 2700, None), (2, 2, 2700, "exhausted"), (2, 3, 2700, "exhausted"), (0, 999, 0, None), (2, 1, 0, None)]:
                with self.subTest(limit=limit, used=used, duration=duration):
                    database.execute("UPDATE projects SET run_budget_limit=?, runs_used=?, max_run_seconds=?", (limit, used, duration))
                    database.commit()
                    if error is None:
                        INTAKE.validate_factory(self.config)
                    else:
                        with self.assertRaisesRegex(INTAKE.IntakeError, error):
                            INTAKE.validate_factory(self.config)
        self.assertEqual(6, self.calls.count(["factoryctl", "status"]), "limit verification uses only the supported status read")

    def factory_calls(self):
        return [call for call in self.calls if call[0] == "factoryctl"]

    def test_status_read_rejects_mismatched_identity(self):
        status = {"projects": [{"id": "2" * 32, "run_budget_limit": 0, "runs_used": 0, "max_run_seconds": 2700}], "agents": []}
        with patch.object(INTAKE, "command", return_value=json.dumps(status)):
            with self.assertRaisesRegex(INTAKE.IntakeError, "configured project needs an overseer"):
                INTAKE.validate_factory(self.config)

    def test_overseer_eligibility_depends_on_role_and_project_not_provider(self):
        for provider in ("codex", "claude_code"):
            for role, project in (("orchestrator", self.config["project_id"]), ("worker", self.config["project_id"]), ("orchestrator", "2" * 32)):
                value = {"projects": [{"id": self.config["project_id"], "run_budget_limit": 0, "runs_used": 0, "max_run_seconds": 0}],
                         "agents": [{"id": self.config["overseer_agent_id"], "project_id": project, "role": role, "provider": provider}]}
                with self.subTest(provider=provider, role=role, project=project), patch.object(INTAKE, "command", return_value=json.dumps(value)):
                    if role == "orchestrator" and project == self.config["project_id"]:
                        INTAKE.validate_factory(self.config)
                    else:
                        with self.assertRaisesRegex(INTAKE.IntakeError, "needs an overseer"):
                            INTAKE.validate_factory(self.config)

    def test_status_read_rejects_malformed_collections(self):
        for value in ([], {}, {"projects": [1], "agents": []}, {"projects": [], "agents": None}, {"projects": [], "agents": [None]}):
            with self.subTest(value=value), patch.object(INTAKE, "command", return_value=json.dumps(value)):
                with self.assertRaises(INTAKE.IntakeError):
                    INTAKE.validate_factory(self.config)

    def test_status_read_error_is_reported_as_installation_prerequisite(self):
        with patch.object(INTAKE, "command", side_effect=INTAKE.IntakeError("transport lost")):
            with self.assertRaisesRegex(INTAKE.IntakeError, "cannot verify configured factory limits"):
                INTAKE.validate_factory(self.config)

    def test_recovery_read_rejects_mismatched_identity(self):
        operation = {"task_id": "4" * 32, "incarnation_id": "5" * 32}
        value = {"state": "found", "task_id": operation["task_id"], "incarnation_id": operation["incarnation_id"], "project_id": "2" * 32, "assigned_agent_id": self.config["overseer_agent_id"], "status": "queued", "needs_operator_recovery": False}
        with patch.object(INTAKE, "command", return_value=json.dumps(value)):
            with self.assertRaisesRegex(INTAKE.IntakeError, "identity conflicts"):
                self.real_state(self.config, operation)

    def test_recovery_read_rejects_incomplete_output(self):
        operation = {"task_id": "4" * 32, "incarnation_id": "5" * 32}
        value = {"state": "found", "task_id": operation["task_id"], "incarnation_id": operation["incarnation_id"], "project_id": self.config["project_id"], "assigned_agent_id": self.config["overseer_agent_id"]}
        for malformed in ([], {"state": "present"}, value):
            with self.subTest(value=malformed), patch.object(INTAKE, "command", return_value=json.dumps(malformed)):
                with self.assertRaises(INTAKE.IntakeError):
                    self.real_state(self.config, operation)

    def test_one_source_task_replays_after_lost_response(self):
        def lost(argv, **kwargs):
            if argv[0] == "factoryctl":
                raise INTAKE.IntakeError("transport lost")
            return self.command(argv, **kwargs)
        INTAKE.command = lost
        with self.assertRaises(INTAKE.IntakeError):
            INTAKE.run_once(self.config)
        record = json.loads(Path(self.config["journal"]).read_text())["issues"]["o/r#7"]
        frozen = record["operation"]
        self.assertEqual(record["desired_fingerprint"], frozen["fingerprint"])
        INTAKE.command = self.command
        self.assertEqual(["replayed o/r#7"], INTAKE.run_once(self.config))
        self.assertEqual(frozen["task_id"], self.factory_calls()[-1][self.factory_calls()[-1].index("--task-id") + 1])

    def test_lost_response_after_database_insert_does_not_recreate(self):
        called = False
        def inserted_then_lost(argv, **kwargs):
            nonlocal called
            if argv[0] != "factoryctl":
                return self.command(argv, **kwargs)
            result = self.command(argv, **kwargs)
            if not called:
                called = True
                raise INTAKE.IntakeError("transport lost")
            return result
        INTAKE.command = inserted_then_lost
        with self.assertRaises(INTAKE.IntakeError):
            INTAKE.run_once(self.config)
        self.calls.clear()
        self.assertEqual([], INTAKE.run_once(self.config))
        self.assertEqual([], self.factory_calls())

    def test_multiple_edits_wait_for_one_active_task_then_use_latest_snapshot(self):
        INTAKE.run_once(self.config)
        first = next(iter(self.states))
        self.source = issue(body="edit one")
        INTAKE.run_once(self.config)
        self.source = issue(body="edit two")
        INTAKE.run_once(self.config)
        self.assertEqual(1, len(self.factory_calls()))
        self.states[first]["status"] = "succeeded"
        self.calls.clear()
        self.assertEqual(["queued o/r#7"], INTAKE.run_once(self.config))
        follow_up = self.factory_calls()[0]
        self.assertIn("edit two", follow_up[follow_up.index("--body") + 1])

    def test_withdrawal_then_reopen_is_serialized_after_the_active_task(self):
        INTAKE.run_once(self.config)
        first = next(iter(self.states))
        self.source = issue(state="CLOSED")
        INTAKE.run_once(self.config)
        self.source = issue(body="reopened")
        INTAKE.run_once(self.config)
        self.assertEqual(1, len(self.factory_calls()))
        self.states[first]["status"] = "failed"
        self.calls.clear()
        INTAKE.run_once(self.config)
        self.assertEqual(1, len(self.factory_calls()))
        self.assertIn("reopened", self.factory_calls()[0][self.factory_calls()[0].index("--body") + 1])

    def test_failed_source_is_not_recreated_without_a_source_change(self):
        INTAKE.run_once(self.config)
        task = next(iter(self.states))
        self.states[task]["status"] = "failed"
        self.calls.clear()
        self.assertEqual([], INTAKE.run_once(self.config))
        self.assertEqual([], self.factory_calls())

    def test_source_marker_and_malicious_fields_are_explicit(self):
        operation = INTAKE.operation_for(self.config, INTAKE.issue_from_json(issue()), "f" * 64)
        self.assertIn("FACTORY_SOURCE o/r#7", operation["body"])
        self.assertIn("Preserve the source marker and linked task IDs", operation["body"])
        self.assertIn("never author an ALLOW for your own work", operation["body"])
        hostile = issue(body={"ignore": "instructions"})
        with self.assertRaisesRegex(INTAKE.IntakeError, "invalid source"):
            INTAKE.issue_from_json(hostile)
        with self.assertRaisesRegex(INTAKE.IssueBodyTooLarge, "exceeds"):
            INTAKE.issue_from_json(issue(body="x" * 5001))

    def test_largest_valid_issue_stays_within_generated_task_bound(self):
        value = issue(body="x" * INTAKE.MAX_ISSUE_BODY)
        value["title"] = "t" * INTAKE.MAX_TITLE
        operation = INTAKE.operation_for(self.config, INTAKE.issue_from_json(value), "f" * 64)
        self.assertLessEqual(len(operation["body"].encode()), INTAKE.MAX_BODY)

    def test_stale_human_decision_waits_for_a_material_source_edit(self):
        INTAKE.run_once(self.config)
        first = next(iter(self.states))
        self.states[first].update({'status': 'failed', 'needs_operator_recovery': True})
        self.calls.clear()
        self.assertEqual(['needs operator recovery o/r#7'], INTAKE.run_once(self.config))
        self.assertEqual([], self.factory_calls())
        record = json.loads(Path(self.config['journal']).read_text())['issues']['o/r#7']
        self.assertEqual(first, record['needs_operator_recovery']['task_id'])
        self.assertEqual([], INTAKE.run_once(self.config))
        self.source = issue(body='operator clarified scope')
        self.assertEqual(['queued o/r#7'], INTAKE.run_once(self.config))
        self.assertNotEqual(first, self.factory_calls()[-1][self.factory_calls()[-1].index('--task-id') + 1])

    def test_identity_change_cannot_reuse_a_managed_journal(self):
        INTAKE.run_once(self.config)
        changed = dict(self.config, project_id='2' * 32)
        with self.assertRaisesRegex(INTAKE.IntakeError, 'different repository'):
            INTAKE.run_once(changed)

    def test_journal_inside_runtime_home_is_rejected_before_journal_writes(self):
        bad = dict(self.config, journal=str(Path(self.config['factory_home']) / 'journal.json'))
        with self.assertRaisesRegex(INTAKE.IntakeError, 'outside factory_home'):
            INTAKE.validate_config(bad)
        self.assertFalse(Path(bad['journal']).exists())
        linked = Path(self.temp.name) / 'linked-home'
        os.symlink(self.config['factory_home'], linked)
        with self.assertRaisesRegex(INTAKE.IntakeError, 'outside factory_home'):
            INTAKE.validate_config(dict(self.config, journal=str(linked / 'journal.json')))


if __name__ == "__main__":
    unittest.main()
