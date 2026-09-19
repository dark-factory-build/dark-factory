#!/usr/bin/env python3
"""Small, offline fixtures for the exact-merge release controller."""
import importlib.util
import io
import json
import os
import runpy
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock


MODULE = Path(__file__).with_name("factory-release.py")
SPEC = importlib.util.spec_from_file_location("factory_release", MODULE)
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)
SITE_SPEC = importlib.util.spec_from_file_location("verify_live_site", MODULE.with_name("verify-live-site.py"))
site = importlib.util.module_from_spec(SITE_SPEC)
SITE_SPEC.loader.exec_module(site)


SHA = "a" * 40
HEAD = "b" * 40
OLD = "c" * 40
SECOND = "d" * 40


def config(journal):
    return {
        "repository": "example/factory",
        "base": "main",
        "journal": str(journal),
        "deploy_argv": ["/bin/true"],
        "verify_argv": ["/bin/true"],
        "review_verifier": [str(MODULE.with_name("verify-adversarial-review.sh"))],
    }


def snapshot():
    return (
        {
            "state": "MERGED",
            "baseRefName": "main",
            "mergeCommitSha": SHA,
            "headRefOid": HEAD,
        },
        SHA,
        [{"commit_id": HEAD, "state": "APPROVED", "user": {"id": 101},
          "body": f"Dark-Factory-Review: allow {HEAD}"}],
        [{"id": 2, "name": "required", "app": {"id": 1},
          "status": "COMPLETED", "conclusion": "SUCCESS"}],
    )


class ReleaseFixtures(unittest.TestCase):
    def test_site_probe_requires_exact_ready_production_deployment(self):
        deployment = {"id": "same", "readyState": "READY", "target": "production", "meta": {"gitCommitSha": SHA}}
        self.assertTrue(site.deployment_healthy(deployment, deployment, SHA, True, True))
        self.assertFalse(site.deployment_healthy(deployment, {**deployment, "id": "moved"}, SHA, True, True))
        self.assertFalse(site.deployment_healthy(deployment, deployment, HEAD, True, True))

    def test_site_probe_command_exits_nonzero_for_the_wrong_deployment(self):
        deployment = {"id": "same", "readyState": "READY", "target": "production", "meta": {"gitCommitSha": HEAD}}
        calls = [
            mock.Mock(stdout=json.dumps(deployment)),
            mock.Mock(returncode=0, stdout='{"healthy":true}'),
            mock.Mock(stdout=json.dumps(deployment)),
        ]
        response = mock.MagicMock()
        response.__enter__.return_value.status = 200
        script = MODULE.with_name("verify-live-site.py")
        with mock.patch.object(sys, "argv", [str(script), SHA]), \
             mock.patch("subprocess.run", side_effect=calls), \
             mock.patch("urllib.request.urlopen", return_value=response), \
             mock.patch("sys.stdout", new=io.StringIO()), \
             self.assertRaises(SystemExit) as raised:
            runpy.run_path(script, run_name="__main__")
        self.assertEqual(raised.exception.code, 1)

    def test_site_probe_command_never_reports_healthy_without_deployment_identity(self):
        script = MODULE.with_name("verify-live-site.py")
        for identity in ({}, {"id": None}, {"id": ""}, {"id": 7}):
            deployment = {"readyState": "READY", "target": "production", "meta": {"gitCommitSha": SHA}, **identity}
            self.assertFalse(site.deployment_healthy(deployment, deployment, SHA, True, True))
            calls = [
                mock.Mock(stdout=json.dumps(deployment)),
                mock.Mock(returncode=0, stdout='{"healthy":true}'),
                mock.Mock(stdout=json.dumps(deployment)),
            ]
            response = mock.MagicMock()
            response.__enter__.return_value.status = 200
            output = io.StringIO()
            with mock.patch.object(sys, "argv", [str(script), SHA]), \
                 mock.patch("subprocess.run", side_effect=calls), \
                 mock.patch("urllib.request.urlopen", return_value=response), \
                 mock.patch("sys.stdout", new=output), \
                 mock.patch("sys.stderr", new=io.StringIO()), \
                 self.assertRaises(SystemExit) as raised:
                runpy.run_path(script, run_name="__main__")
            self.assertEqual(raised.exception.code, 1)
            self.assertEqual(output.getvalue(), "")

    def test_shared_atomic_writer_preserves_receipt_on_replace_failure(self):
        self.assertIs(release.atomic_json, release.intake.atomic_json)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "receipt.json"
            release.atomic_json(path, {"state": "verified"})
            before = path.read_bytes()
            with mock.patch.object(release.intake.os, "replace", side_effect=OSError("injected")):
                with self.assertRaises(OSError):
                    release.atomic_json(path, {"state": "running"})
            self.assertEqual(before, path.read_bytes())
            self.assertEqual([path], list(path.parent.iterdir()))
            self.assertEqual(0o600, path.stat().st_mode & 0o777)

    def test_wrong_sha_verification_is_rejected(self):
        with self.assertRaises(release.ReleaseError):
            release.verify_output(json.dumps({"sha": "c" * 40, "healthy": True}), SHA)

    def test_default_config_keeps_the_existing_receipt_fingerprint(self):
        cfg = config(Path("/tmp/release.json"))
        old = {key: cfg.get(key) for key in ("repository", "base", "deploy_argv", "verify_argv", "review_verifier", "command_timeout")}
        import hashlib
        expected = hashlib.sha256(json.dumps(old, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        self.assertEqual(release.config_fingerprint(cfg), expected)

    def test_unmerged_pr_is_rejected(self):
        pr, default, reviews, checks = snapshot()
        pr["state"] = "OPEN"
        with self.assertRaises(release.ReleaseError):
            release.merge_gate(pr, default, reviews, checks, config(Path("/tmp/release.json")), SHA)

    def test_review_without_exact_allow_is_rejected(self):
        pr, default, reviews, checks = snapshot()
        reviews[0]["body"] = "Dark-Factory-Review: deny"
        with self.assertRaises(release.ReleaseError):
            release.merge_gate(pr, default, reviews, checks, config(Path("/tmp/release.json")), SHA)

    def test_any_positive_numeric_publisher_can_allow_exact_head(self):
        pr, default, reviews, checks = snapshot()
        for author in (101, "202", 987654321):
            reviews[0]["user"]["id"] = author
            self.assertEqual(
                release.merge_gate(pr, default, reviews, checks, config(Path("/tmp/release.json")), SHA),
                HEAD,
            )

    def test_invalid_publishers_and_stale_reviews_do_not_allow(self):
        pr, default, reviews, checks = snapshot()
        for author in (0, -1, "0", "-1", "202.0", True, None):
            reviews[0]["user"]["id"] = author
            with self.assertRaises(release.ReleaseError):
                release.merge_gate(pr, default, reviews, checks, config(Path("/tmp/release.json")), SHA)
        reviews[0]["user"]["id"] = 1
        reviews[0]["state"] = "PENDING"
        with self.assertRaises(release.ReleaseError):
            release.merge_gate(pr, default, reviews, checks, config(Path("/tmp/release.json")), SHA)
        reviews[0]["state"] = "DISMISSED"
        with self.assertRaises(release.ReleaseError):
            release.merge_gate(pr, default, reviews, checks, config(Path("/tmp/release.json")), SHA)

    def test_block_precedes_allow_and_exact_correction_clears_it(self):
        pr, default, reviews, checks = snapshot()
        operation = "11111111-1111-4111-8111-111111111111"
        reviews[:] = [
            {"commit_id": HEAD, "state": "COMMENTED", "user": {"id": 1},
             "body": f"Dark-Factory-Review: block {HEAD} <!-- dark-factory-operation:{operation}:old -->"},
            {"commit_id": HEAD, "state": "COMMENTED", "user": {"id": 1},
             "body": f"Dark-Factory-Review: allow {HEAD} Dark-Factory-Review-Correction: {operation} <!-- dark-factory-operation:22222222-2222-4222-8222-222222222222:new -->"},
        ]
        self.assertEqual(
            release.merge_gate(pr, default, reviews, checks, config(Path("/tmp/release.json")), SHA),
            HEAD,
        )
        reviews[1]["user"]["id"] = 2
        with self.assertRaises(release.ReleaseError):
            release.merge_gate(pr, default, reviews, checks, config(Path("/tmp/release.json")), SHA)
        reviews[1]["user"]["id"] = 1
        reviews[1]["body"] = f"Dark-Factory-Review: allow {HEAD}"
        with self.assertRaises(release.ReleaseError):
            release.merge_gate(pr, default, reviews, checks, config(Path("/tmp/release.json")), SHA)

    def test_failed_check_is_rejected(self):
        pr, default, reviews, checks = snapshot()
        checks[0]["conclusion"] = "FAILURE"
        with self.assertRaises(release.ReleaseError):
            release.merge_gate(pr, default, reviews, checks, config(Path("/tmp/release.json")), SHA)

    def test_superseded_failed_check_is_ignored(self):
        pr, default, reviews, checks = snapshot()
        checks.append({**checks[0], "id": 1, "conclusion": "FAILURE"})
        self.assertEqual(
            release.merge_gate(pr, default, reviews, checks, config(Path("/tmp/release.json")), SHA),
            HEAD,
        )

    def test_same_named_check_from_another_app_and_duplicate_ids_fail_closed(self):
        pr, default, reviews, checks = snapshot()
        checks.append({**checks[0], "id": 3, "app": {"id": 2}, "conclusion": "FAILURE"})
        with self.assertRaises(release.ReleaseError):
            release.merge_gate(pr, default, reviews, checks, config(Path("/tmp/release.json")), SHA)
        checks[-1] = {**checks[0], "conclusion": "FAILURE"}
        with self.assertRaises(release.ReleaseError):
            release.merge_gate(pr, default, reviews, checks, config(Path("/tmp/release.json")), SHA)

    def test_live_already_records_verified_without_deploy(self):
        with tempfile.TemporaryDirectory() as directory:
            cfg = config(Path(directory) / "release.json")
            with mock.patch.object(release, "gh_snapshot", return_value=snapshot()), \
                 mock.patch.object(release, "review_gate") as verifier, \
                 mock.patch.object(release, "probe", return_value={"sha": SHA, "healthy": True}), \
                 mock.patch.object(release, "run") as command:
                result = release.once(cfg, 633)
            self.assertEqual(result["state"], "verified")
            verifier.assert_called_once_with(cfg, HEAD, snapshot()[2])
            command.assert_not_called()

    def test_reconcile_records_verified_receipt_without_deploy_hook(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            release.atomic_json(journal, {"version": 1, "live_tip": {"sha": OLD, "healthy": True}, "releases": {}})
            with mock.patch.object(release, "gh_snapshot", return_value=snapshot()), \
                 mock.patch.object(release, "review_gate") as verifier, \
                 mock.patch.object(release, "probe", return_value={"sha": SHA, "healthy": True}), \
                 mock.patch.object(release, "range_sources", return_value=([{"pr": 633, "merge_sha": SHA, "issue": 602, "reference": "refs"}], "range")), \
                 mock.patch.object(release, "run", return_value=json.dumps({"status": "ahead", "merge_base_commit": {"sha": SHA}})) as command:
                result = release.reconcile(cfg, 633, SHA)
            self.assertEqual(result["state"], "verified")
            self.assertEqual(release.load(journal)["live_tip"]["sha"], SHA)
            self.assertEqual(result["reconciliation"], {"mode": "operator_observed", "observed_sha": SHA})
            verifier.assert_called_once_with(cfg, HEAD, snapshot()[2])
            command.assert_called_once()

    def test_reconcile_supersedes_multiple_historical_blocked_receipts(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            old_sha, older_sha = OLD, "e" * 40
            old = {"pr": 631, "sha": old_sha, "state": "blocked", "error": "old failure",
                   "config_fingerprint": "f" * 64, "history": ["kept"]}
            older = {"pr": 632, "sha": older_sha, "state": "blocked", "error": "older failure",
                     "config_fingerprint": "e" * 64}
            release.atomic_json(journal, {"version": 1, "live_tip": {"sha": OLD, "healthy": True},
                                          "releases": {"631": old, "632": older}})
            current = snapshot()
            old_snapshot = tuple([dict(current[0], mergeCommitSha=old_sha), old_sha, [], current[3]])
            older_snapshot = tuple([dict(current[0], mergeCommitSha=older_sha), older_sha, [], current[3]])
            def compare(argv, *args, **kwargs):
                target = next(part for part in argv if "..." in part).split("/")[-1].split("...")[0]
                return json.dumps({"status": "ahead", "merge_base_commit": {"sha": target}})
            with mock.patch.object(release, "gh_snapshot", side_effect=[current, old_snapshot, older_snapshot]), \
                 mock.patch.object(release, "review_gate"), \
                 mock.patch.object(release, "probe", return_value={"sha": SHA, "healthy": True}), \
                 mock.patch.object(release, "run", side_effect=compare):
                result = release.reconcile(cfg, 633, SHA, [631, 632], baseline_current=True)
            saved = release.load(journal)
            self.assertEqual(result["delivery_mode"], "baseline_current")
            self.assertEqual(saved["releases"]["631"]["error"], "old failure")
            self.assertEqual(saved["releases"]["631"]["superseded_by"], {"pr": 633, "sha": SHA,
                                                                     "source_pr": 631, "source_sha": old_sha})
            self.assertEqual(saved["releases"]["632"]["config_fingerprint"], "e" * 64)
            self.assertEqual(saved["releases"]["633"]["reconciliation"]["baseline_sha"], OLD)
            for named in ([631, 632], []):
                with mock.patch.object(release, "gh_snapshot", side_effect=[current, old_snapshot, older_snapshot]), \
                     mock.patch.object(release, "review_gate"), \
                     mock.patch.object(release, "probe", return_value={"sha": SHA, "healthy": True}), \
                     mock.patch.object(release, "run", side_effect=compare):
                    release.reconcile(cfg, 633, SHA, named, baseline_current=bool(named))
            for number, original in ((631, old), (632, older)):
                preserved = dict(release.load(journal)["releases"][str(number)])
                preserved.pop("superseded_by")
                self.assertEqual(preserved, original)
            with mock.patch.object(release, "gh_snapshot", return_value=current), \
                 mock.patch.object(release, "review_gate"), \
                 mock.patch.object(release, "probe", return_value={"sha": SHA, "healthy": True}), \
                 mock.patch.object(release, "run") as command:
                self.assertEqual(release.once(cfg, 633)["state"], "verified")
            command.assert_not_called()
            repeated = release.load(journal)["releases"]["633"]["reconciliation"]
            self.assertEqual(repeated["superseded_prs"], [{"pr": 631, "sha": old_sha}, {"pr": 632, "sha": older_sha}])
            self.assertEqual(repeated["baseline_live_tip"]["sha"], OLD)
            self.assertTrue(repeated["baseline_live_tip"]["healthy"])

    def test_supersede_rejects_active_or_malformed_or_diverged_receipt_without_write(self):
        for old_state, old_sha, comparison in (("running", OLD, "ahead"), ("blocked", "bad", "ahead"), ("blocked", OLD, "diverged")):
            with self.subTest(old_state=old_state, old_sha=old_sha, comparison=comparison), tempfile.TemporaryDirectory() as directory:
                journal = Path(directory) / "release.json"
                cfg = config(journal)
                before = {"version": 1, "releases": {"631": {"pr": 631, "sha": old_sha,
                    "state": old_state, "error": "keep", "config_fingerprint": "f" * 64}}}
                release.atomic_json(journal, before)
                current = snapshot()
                old_snapshot = tuple([dict(current[0], mergeCommitSha=OLD), OLD, [], current[3]])
                def compare(argv, *args, **kwargs):
                    base = argv[2].split("/")[-1].split("...")[0]
                    return json.dumps({"status": comparison if base == OLD else "identical",
                                       "merge_base_commit": {"sha": base}})
                with mock.patch.object(release, "gh_snapshot", side_effect=[current, old_snapshot]), \
                     mock.patch.object(release, "review_gate"), \
                     mock.patch.object(release, "probe", return_value={"sha": SHA, "healthy": True}), \
                     mock.patch.object(release, "run", side_effect=compare):
                    with self.assertRaises(release.ReleaseError):
                        release.reconcile(cfg, 633, SHA, [631], baseline_current=True)
                self.assertEqual(release.load(journal), before)

    def test_superseded_receipt_is_only_ignored_with_verified_reconciliation_target(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            old = {"pr": 631, "sha": OLD, "state": "blocked", "config_fingerprint": "f" * 64,
                   "superseded_by": {"pr": 633, "sha": SHA, "source_sha": OLD}}
            target = {"pr": 633, "sha": SHA, "state": "verified",
                      "verification": {"sha": SHA, "healthy": True},
                      "reconciliation": {"mode": "operator_observed", "observed_sha": SHA,
                                          "superseded_prs": [{"pr": 631, "sha": OLD}]}}
            old["superseded_by"]["source_pr"] = 631
            release.atomic_json(journal, {"version": 1, "releases": {"631": old, "633": target}})
            newer = tuple([dict(snapshot()[0], mergeCommitSha=SECOND), SECOND, snapshot()[2], snapshot()[3]])
            with mock.patch.object(release, "gh_snapshot", return_value=newer), \
                 mock.patch.object(release, "review_gate"), \
                 mock.patch.object(release, "probe", return_value={"sha": SECOND, "healthy": True}), \
                 mock.patch.object(release, "range_sources", return_value=([], "range")), \
                 mock.patch.object(release, "run") as command:
                result = release.once(cfg, 634)
            self.assertEqual(result["state"], "verified")
            command.assert_not_called()
            for invalid in (None, {}, [], [None]):
                target["reconciliation"]["superseded_prs"] = invalid
                release.atomic_json(journal, {"version": 1, "releases": {"631": old, "633": target}})
                with mock.patch.object(release, "gh_snapshot") as github, \
                     mock.patch.object(release, "run") as command:
                    with self.assertRaises(release.ReleaseError):
                        release.once(cfg, 634)
                github.assert_not_called()
                command.assert_not_called()

    def test_reconcile_accepts_reviewed_ancestor_when_default_advanced(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            release.atomic_json(journal, {"version": 1, "live_tip": {"sha": OLD, "healthy": True}, "releases": {}})
            current = (dict(snapshot()[0], mergeCommitSha=SHA), SECOND, snapshot()[2], snapshot()[3])
            with mock.patch.object(release, "gh_snapshot", return_value=current), \
                 mock.patch.object(release, "review_gate"), \
                 mock.patch.object(release, "probe", return_value={"sha": SHA, "healthy": True}), \
                 mock.patch.object(release, "range_sources", return_value=([], "range")), \
                 mock.patch.object(release, "run", return_value=json.dumps({"status": "ahead", "merge_base_commit": {"sha": SHA}})):
                result = release.reconcile(cfg, 633, SHA)
            self.assertEqual(result["state"], "verified")

    def test_reconcile_rejects_diverged_target_without_writing_or_hook(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            before = {"version": 1, "live_tip": {"sha": OLD, "healthy": True}, "releases": {}}
            release.atomic_json(journal, before)
            current = (dict(snapshot()[0], mergeCommitSha=SHA), SECOND, snapshot()[2], snapshot()[3])
            with mock.patch.object(release, "gh_snapshot", return_value=current), \
                 mock.patch.object(release, "review_gate"), \
                 mock.patch.object(release, "run", return_value=json.dumps({"status": "diverged"})) as command:
                with self.assertRaisesRegex(release.ReleaseError, "not an ancestor"):
                    release.reconcile(cfg, 633, SHA)
            self.assertEqual(release.load(journal), before)
            command.assert_called_once()

    def test_normal_deployment_still_rejects_stale_merged_target(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            before = {"version": 1, "live_tip": {"sha": OLD, "healthy": True}, "releases": {}}
            release.atomic_json(journal, before)
            current = (dict(snapshot()[0], mergeCommitSha=SHA), SECOND, snapshot()[2], snapshot()[3])
            with mock.patch.object(release, "gh_snapshot", return_value=current), \
                 mock.patch.object(release, "review_gate"), mock.patch.object(release, "run") as command:
                with self.assertRaisesRegex(release.ReleaseError, "not at the merged SHA"):
                    release.once(cfg, 633)
            self.assertEqual(release.load(journal), before)
            command.assert_not_called()

    def test_reconcile_rejects_unhealthy_or_wrong_live_probe_without_writing(self):
        for observed in ({"sha": SHA, "healthy": False}, {"sha": OLD, "healthy": True}):
            with self.subTest(observed=observed), tempfile.TemporaryDirectory() as directory:
                journal = Path(directory) / "release.json"
                cfg = config(journal)
                before = {"version": 1, "live_tip": {"sha": OLD, "healthy": True}, "releases": {}}
                release.atomic_json(journal, before)
                with mock.patch.object(release, "gh_snapshot", return_value=snapshot()), \
                     mock.patch.object(release, "review_gate"), \
                     mock.patch.object(release, "probe", return_value=observed), \
                     mock.patch.object(release, "run", return_value=json.dumps({"status": "ahead", "merge_base_commit": {"sha": SHA}})) as command:
                    with self.assertRaisesRegex(release.ReleaseError, "healthy SHA"):
                        release.reconcile(cfg, 633, SHA)
                self.assertEqual(release.load(journal), before)
                command.assert_called_once()

    def test_reconcile_rejects_fingerprint_mismatch_without_writing(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            before = {"version": 1, "live_tip": {"sha": OLD, "healthy": True}, "releases": {
                "633": {"pr": 633, "sha": SHA, "state": "blocked", "config_fingerprint": "f" * 64}}}
            release.atomic_json(journal, before)
            with mock.patch.object(release, "gh_snapshot") as snapshot_call, mock.patch.object(release, "run") as command:
                with self.assertRaisesRegex(release.ReleaseError, "different repository"):
                    release.reconcile(cfg, 633, SHA)
            self.assertEqual(release.load(journal), before)
            snapshot_call.assert_not_called()
            command.assert_not_called()

    def test_reconcile_failed_range_mapping_does_not_write_or_deploy(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            before = {"version": 1, "live_tip": {"sha": OLD, "healthy": True}, "releases": {}}
            release.atomic_json(journal, before)
            with mock.patch.object(release, "gh_snapshot", return_value=snapshot()), \
                 mock.patch.object(release, "review_gate"), \
                 mock.patch.object(release, "probe", return_value={"sha": SHA, "healthy": True}), \
                 mock.patch.object(release, "range_sources", side_effect=release.ReleaseError("range incomplete")), \
                 mock.patch.object(release, "run", return_value=json.dumps({"status": "ahead", "merge_base_commit": {"sha": SHA}})) as command:
                with self.assertRaisesRegex(release.ReleaseError, "range incomplete"):
                    release.reconcile(cfg, 633, SHA)
            self.assertEqual(release.load(journal), before)
            command.assert_called_once()

    def test_reconcile_rejects_wrong_observed_sha_without_writing(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            before = {"version": 1, "live_tip": {"sha": OLD, "healthy": True}, "releases": {}}
            release.atomic_json(journal, before)
            with mock.patch.object(release, "gh_snapshot", return_value=snapshot()), \
                 mock.patch.object(release, "review_gate"), \
                 mock.patch.object(release, "run") as command:
                with self.assertRaisesRegex(release.ReleaseError, "does not match"):
                    release.reconcile(cfg, 633, OLD)
            self.assertEqual(release.load(journal), before)
            command.assert_not_called()

    def test_reconcile_refuses_uncertain_receipt_without_hook(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            release.atomic_json(journal, {"version": 1, "releases": {
                "632": {"pr": 632, "sha": OLD, "state": "running",
                         "config_fingerprint": release.config_fingerprint(cfg)}}})
            with mock.patch.object(release, "gh_snapshot") as snapshot_call, \
                 mock.patch.object(release, "run") as command:
                with self.assertRaisesRegex(release.ReleaseError, "unresolved running"):
                    release.reconcile(cfg, 633, SHA)
            snapshot_call.assert_not_called()
            command.assert_not_called()

    def test_running_release_with_unknown_probe_becomes_blocked(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            release.atomic_json(journal, {"version": 1, "releases": {
                "633": {"pr": 633, "sha": SHA, "state": "running",
                         "config_fingerprint": release.config_fingerprint(cfg)}
            }})
            with mock.patch.object(release, "probe", side_effect=release.ReleaseError("probe unavailable")):
                with self.assertRaises(release.ReleaseError):
                    release.once(cfg, 633)
            receipt = release.load(journal)["releases"]["633"]
            self.assertEqual(receipt["state"], "blocked")
            self.assertIn("ambiguous", receipt["error"])

    def test_running_release_with_healthy_old_sha_becomes_blocked(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            release.atomic_json(journal, {"version": 1, "releases": {
                "633": {"pr": 633, "sha": SHA, "state": "running",
                         "config_fingerprint": release.config_fingerprint(cfg)}
            }})
            with mock.patch.object(release, "probe", return_value={"sha": "c" * 40, "healthy": True}):
                with self.assertRaises(release.ReleaseError):
                    release.once(cfg, 633)
            self.assertEqual(release.load(journal)["releases"]["633"]["state"], "blocked")

    def test_unresolved_old_release_blocks_newer_latest_without_hook_or_dispatch(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            entry = {"pr": 633, "sha": SHA, "state": "blocked",
                     "config_fingerprint": release.config_fingerprint(cfg)}
            release.atomic_json(journal, {"version": 1, "releases": {"633": entry},
                                          "unresolved_deployment": {
                                              "pr": 633, "sha": SHA,
                                              "config_fingerprint": release.config_fingerprint(cfg),
                                              "error": "deployment outcome is ambiguous",
                                          }})
            with mock.patch.object(release, "gh_snapshot") as snapshot_call, \
                 mock.patch.object(release, "run") as command:
                with self.assertRaisesRegex(release.ReleaseError, "earlier deployment is unresolved"):
                    release.once(cfg, 634)
            snapshot_call.assert_not_called()
            command.assert_not_called()

    def test_legacy_running_receipt_blocks_newer_release_before_github_planning(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            release.atomic_json(journal, {"version": 1, "releases": {
                "633": {"pr": 633, "sha": SHA, "state": "running",
                         "config_fingerprint": release.config_fingerprint(cfg)}
            }})
            with mock.patch.object(release, "gh_snapshot") as snapshot_call, \
                 mock.patch.object(release, "run") as command:
                with self.assertRaisesRegex(release.ReleaseError, "earlier deployment is unresolved"):
                    release.once(cfg, 634)
            snapshot_call.assert_not_called()
            command.assert_not_called()
            self.assertEqual(release.load(journal)["unresolved_deployment"]["pr"], 633)

    def test_legacy_running_receipt_with_different_config_blocks_newer_release(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            old_cfg = dict(cfg, deploy_argv=["/bin/false"])
            original = {"version": 1, "releases": {
                "633": {"pr": 633, "sha": SHA, "state": "running",
                         "config_fingerprint": release.config_fingerprint(old_cfg)}
            }}
            release.atomic_json(journal, original)
            with mock.patch.object(release, "gh_snapshot") as snapshot_call, \
                 mock.patch.object(release, "run") as command:
                with self.assertRaisesRegex(release.ReleaseError, "different repository or hook configuration"):
                    release.once(cfg, 634)
            snapshot_call.assert_not_called()
            command.assert_not_called()
            self.assertEqual(release.load(journal), original)

    def test_legacy_blocked_receipt_blocks_newer_release_before_github_planning(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            fingerprint = release.config_fingerprint(cfg)
            release.atomic_json(journal, {"version": 1, "releases": {
                "633": {"pr": 633, "sha": SHA, "state": "blocked",
                         "config_fingerprint": fingerprint}
            }})
            with mock.patch.object(release, "gh_snapshot") as snapshot_call, \
                 mock.patch.object(release, "run") as command:
                with self.assertRaisesRegex(release.ReleaseError, "earlier deployment is unresolved"):
                    release.once(cfg, 634)
            snapshot_call.assert_not_called()
            command.assert_not_called()
            self.assertEqual(release.load(journal)["unresolved_deployment"]["pr"], 633)

    def test_legacy_opaque_blocked_receipt_from_old_config_blocks_newer_release(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            old_cfg = dict(cfg, deploy_argv=["/bin/false"])
            entry = {"pr": 633, "sha": SHA, "state": "blocked",
                     "config_fingerprint": release.config_fingerprint(old_cfg),
                     "error": "deployment outcome is ambiguous"}
            release.atomic_json(journal, {"version": 1, "releases": {"633": entry}})
            with mock.patch.object(release, "gh_snapshot") as snapshot_call, \
                 mock.patch.object(release, "run") as command:
                with self.assertRaisesRegex(release.ReleaseError, "earlier deployment is unresolved"):
                    release.once(cfg, 634)
            snapshot_call.assert_not_called()
            command.assert_not_called()
            self.assertEqual(release.load(journal)["unresolved_deployment"]["pr"], 633)

    def test_reconcile_settles_exact_healthy_deployment_without_hook(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            fingerprint = release.config_fingerprint(dict(cfg, deploy_argv=["/bin/false"]))
            release.atomic_json(journal, {"version": 1, "releases": {
                "633": {"pr": 633, "sha": SHA, "state": "blocked",
                         "config_fingerprint": fingerprint}},
                "live_tip": {"sha": OLD, "healthy": True},
                "unresolved_deployment": {"pr": 633, "sha": SHA,
                                            "config_fingerprint": fingerprint,
                                            "error": "ambiguous"}})
            with mock.patch.object(release, "gh_snapshot", return_value=snapshot()), \
                 mock.patch.object(release, "review_gate"), \
                 mock.patch.object(release, "probe", return_value={"sha": SHA, "healthy": True}), \
                 mock.patch.object(release, "range_sources", return_value=([{"pr": 633, "merge_sha": SHA,
                                                                                "issue": 602, "reference": "refs"}], "range")), \
                 mock.patch.object(release, "run", return_value=json.dumps({"status": "ahead", "merge_base_commit": {"sha": SHA}})) as command:
                result = release.reconcile(cfg, 633, SHA)
            self.assertEqual(result["state"], "verified")
            self.assertNotIn("unresolved_deployment", release.load(journal))
            self.assertEqual(command.call_count, 1)
            self.assertEqual(command.call_args.args[0][:2], ["gh", "api"])

    def test_legacy_predeploy_blocked_receipt_can_be_recovered(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            fingerprint = release.config_fingerprint(cfg)
            release.atomic_json(journal, {"version": 1, "releases": {
                "633": {"pr": 633, "sha": SHA, "state": "blocked",
                         "config_fingerprint": fingerprint,
                         "error": "live probe unavailable or malformed before deployment"}
            }})
            with mock.patch.object(release, "gh_snapshot", return_value=snapshot()), \
                 mock.patch.object(release, "review_gate"), \
                 mock.patch.object(release, "probe", return_value={"sha": SHA, "healthy": True}), \
                 mock.patch.object(release, "run") as command:
                result = release.once(cfg, 633, retry=True)
            self.assertEqual(result["state"], "verified")
            command.assert_not_called()
            self.assertNotIn("unresolved_deployment", release.load(journal))

    def test_hook_crash_leaves_barrier_before_any_hook_effect(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            def hook_crash(argv, *args, **kwargs):
                if argv[:2] == ["gh", "api"]:
                    return json.dumps({"object": {"sha": SHA}})
                raise KeyboardInterrupt
            with mock.patch.object(release, "gh_snapshot", return_value=snapshot()), \
                 mock.patch.object(release, "review_gate"), \
                 mock.patch.object(release, "probe", return_value={"sha": OLD, "healthy": True}), \
                 mock.patch.object(release, "range_sources", return_value=([{"pr": 633, "merge_sha": SHA, "issue": 602, "reference": "refs"}], "range")), \
                 mock.patch.object(release, "run", side_effect=hook_crash):
                with self.assertRaises(KeyboardInterrupt):
                    release.once(cfg, 633)
            receipt = release.load(journal)["releases"]["633"]
            self.assertEqual(receipt["state"], "running")
            self.assertEqual(release.load(journal)["unresolved_deployment"]["sha"], SHA)

    def test_explicit_recovery_clears_barrier_and_allows_subsequent_release(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            fingerprint = release.config_fingerprint(cfg)
            release.atomic_json(journal, {"version": 1, "releases": {
                "633": {"pr": 633, "sha": SHA, "state": "blocked", "config_fingerprint": fingerprint}},
                "live_tip": {"sha": OLD, "healthy": True},
                "unresolved_deployment": {"pr": 633, "sha": SHA, "config_fingerprint": fingerprint,
                                            "error": "ambiguous"}})
            with mock.patch.object(release, "gh_snapshot", return_value=snapshot()), \
                 mock.patch.object(release, "review_gate"), \
                 mock.patch.object(release, "probe", return_value={"sha": SHA, "healthy": True}), \
                 mock.patch.object(release, "run") as command:
                release.once(cfg, 633, retry=True)
            command.assert_not_called()
            self.assertNotIn("unresolved_deployment", release.load(journal))

            newer = snapshot()
            newer = (dict(newer[0], mergeCommitSha=SECOND), SECOND, newer[2], newer[3])
            with mock.patch.object(release, "gh_snapshot", return_value=newer), \
                 mock.patch.object(release, "review_gate"), \
                 mock.patch.object(release, "probe", return_value={"sha": SHA, "healthy": True}), \
                 mock.patch.object(release, "range_sources", return_value=([{"pr": 634, "merge_sha": SECOND, "issue": 602, "reference": "refs"}], "range")), \
                 mock.patch.object(release, "verify", return_value={"sha": SECOND, "healthy": True}), \
                 mock.patch.object(release, "run", side_effect=lambda argv, *a, **kw: json.dumps({"object": {"sha": SECOND}}) if argv[:2] == ["gh", "api"] else ""):
                result = release.once(cfg, 634)
            self.assertEqual(result["state"], "verified")

    def test_failed_explicit_recovery_never_replans_or_deploys(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            fingerprint = release.config_fingerprint(cfg)
            release.atomic_json(journal, {"version": 1, "releases": {
                "633": {"pr": 633, "sha": SHA, "state": "blocked", "config_fingerprint": fingerprint}},
                "unresolved_deployment": {"pr": 633, "sha": SHA, "config_fingerprint": fingerprint,
                                            "error": "ambiguous"}})
            with mock.patch.object(release, "probe", side_effect=release.ReleaseError("probe unavailable")), \
                 mock.patch.object(release, "gh_snapshot") as snapshot_call, \
                 mock.patch.object(release, "run") as command:
                with self.assertRaisesRegex(release.ReleaseError, "ambiguous"):
                    release.once(cfg, 633, retry=True)
            snapshot_call.assert_not_called()
            command.assert_not_called()

    def test_ambiguous_running_release_is_not_replayed(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            release.atomic_json(journal, {"version": 1, "releases": {
                "633": {"pr": 633, "sha": SHA, "state": "running",
                         "config_fingerprint": release.config_fingerprint(cfg)}}})
            with mock.patch.object(release, "probe", return_value={"sha": OLD, "healthy": True}), \
                 mock.patch.object(release, "run") as command:
                with self.assertRaisesRegex(release.ReleaseError, "ambiguous"):
                    release.once(cfg, 633, retry=True)
            command.assert_not_called()
            self.assertIn("unresolved_deployment", release.load(journal))

    def test_unavailable_predeploy_probe_never_deploys(self):
        with tempfile.TemporaryDirectory() as directory:
            cfg = config(Path(directory) / "release.json")
            with mock.patch.object(release, "gh_snapshot", return_value=snapshot()), \
                 mock.patch.object(release, "review_gate"), \
                 mock.patch.object(release, "probe", side_effect=release.ReleaseError("probe unavailable")), \
                 mock.patch.object(release, "run") as command:
                with self.assertRaises(release.ReleaseError):
                    release.once(cfg, 633)
            self.assertEqual(release.load(Path(cfg["journal"]))["releases"]["633"]["state"], "blocked")
            command.assert_not_called()

    def test_real_review_gate_flattens_multiline_allow(self):
        release.review_gate(config(Path("/tmp/release.json")), HEAD, [{
            "commit_id": HEAD,
            "state": "APPROVED",
            "user": {"id": 101},
            "body": f"findings\nDark-Factory-Review:\tallow {HEAD}\nfinal",
        }])

    def test_real_review_gate_rejects_block(self):
        with self.assertRaises(release.ReleaseError):
            release.review_gate(config(Path("/tmp/release.json")), HEAD, [{
                "commit_id": HEAD,
                "state": "CHANGES_REQUESTED",
                "user": {"id": 202},
                "body": f"findings\nDark-Factory-Review: block {HEAD}",
            }])

    def test_range_records_every_explicit_source_footer(self):
        cfg = config(Path("/tmp/release.json"))
        def gh(argv, *unused):
            command = " ".join(argv)
            if "/compare/" in command:
                return json.dumps({"status": "ahead", "total_commits": 2,
                                   "commits": [{"sha": SHA}, {"sha": SECOND}]})
            if SHA + "/pulls" in command:
                return json.dumps([[{"number": 10, "merge_commit_sha": SHA, "merged_at": "now", "base": {"ref": "main"}}]])
            if SECOND + "/pulls" in command:
                return json.dumps([[{"number": 11, "merge_commit_sha": SECOND, "merged_at": "now", "base": {"ref": "main"}}]])
            if "pr view 10" in command:
                return json.dumps({"state": "MERGED", "baseRefName": "main", "mergeCommit": {"oid": SHA}, "body": "Changes\n\nRefs #602\n"})
            if "pr view 11" in command:
                return json.dumps({"state": "MERGED", "baseRefName": "main", "mergeCommit": {"oid": SECOND}, "body": "Changes\n\nCloses #512\n"})
            self.fail(command)
        with mock.patch.object(release, "run", side_effect=gh):
            sources, mode = release.range_sources(cfg, OLD, SECOND)
        self.assertEqual(mode, "range")
        self.assertEqual(sources, [{"pr": 10, "merge_sha": SHA, "issue": 602, "reference": "refs"},
                                   {"pr": 11, "merge_sha": SECOND, "issue": 512, "reference": "closes"}])

    def test_range_accepts_crlf_source_footer(self):
        cfg = config(Path("/tmp/release.json"))
        def gh(argv, *unused):
            command = " ".join(argv)
            if "/compare/" in command:
                return json.dumps({"status": "ahead", "total_commits": 1, "commits": [{"sha": SHA}]})
            if "/pulls" in command:
                return json.dumps([[{"number": 10, "merge_commit_sha": SHA, "merged_at": "now", "base": {"ref": "main"}}]])
            return json.dumps({"state": "MERGED", "baseRefName": "main", "mergeCommit": {"oid": SHA},
                               "body": "Work\r\n\r\nRefs #602\r\n"})
        with mock.patch.object(release, "run", side_effect=gh):
            self.assertEqual(release.range_sources(cfg, OLD, SHA)[0][0]["issue"], 602)

    def test_release_rejects_malformed_source_reference(self):
        for body in ("Refs #602 extra", "Refs #0", "Refs #602foo", "Closes #unknown"):
            with self.subTest(body=body), self.assertRaises(release.ReleaseError):
                release.release_source_footer(body)

    def test_range_skips_source_less_pull_request(self):
        cfg = config(Path("/tmp/release.json"))
        def gh(argv, *unused):
            command = " ".join(argv)
            if "/compare/" in command:
                return json.dumps({"status": "ahead", "total_commits": 1, "commits": [{"sha": SHA}]})
            if "/pulls" in command:
                return json.dumps([[{"number": 10, "merge_commit_sha": SHA, "merged_at": "now", "base": {"ref": "main"}}]])
            return json.dumps({"state": "MERGED", "baseRefName": "main", "mergeCommit": {"oid": SHA}, "body": "No source"})
        with mock.patch.object(release, "run", side_effect=gh):
            self.assertEqual(release.range_sources(cfg, OLD, SHA), ([], "range"))

    def test_range_accepts_unique_footer_with_generator_trailer_and_deduplicates(self):
        cfg = config(Path("/tmp/release.json"))
        def gh(argv, *unused):
            command = " ".join(argv)
            if "/compare/" in command:
                return json.dumps({"status": "ahead", "total_commits": 1, "commits": [{"sha": SHA}]})
            if "/pulls" in command:
                return json.dumps([[{"number": 10, "merge_commit_sha": SHA, "merged_at": "now", "base": {"ref": "main"}}]])
            return json.dumps({"state": "MERGED", "baseRefName": "main", "mergeCommit": {"oid": SHA},
                               "body": "Refs #602\n\nRefs #602\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)\n"})
        with mock.patch.object(release, "run", side_effect=gh):
            self.assertEqual(release.range_sources(cfg, OLD, SHA)[0],
                             [{"pr": 10, "merge_sha": SHA, "issue": 602, "reference": "refs"}])

    def test_qualified_source_footer_preserves_repository(self):
        self.assertEqual(('refs', 7, 'other/backlog'), release.release_source_footer('Summary\nRefs other/backlog#7'))
        self.assertEqual(('closes', 7, None), release.release_source_footer('Summary\nCloses #7'))

    def test_range_uses_existing_terminal_footer_precedence(self):
        cfg = config(Path("/tmp/release.json"))
        def gh(argv, *unused):
            command = " ".join(argv)
            if "/compare/" in command:
                return json.dumps({"status": "ahead", "total_commits": 1, "commits": [{"sha": SHA}]})
            if "/pulls" in command:
                return json.dumps([[{"number": 10, "merge_commit_sha": SHA, "merged_at": "now", "base": {"ref": "main"}}]])
            return json.dumps({"state": "MERGED", "baseRefName": "main", "mergeCommit": {"oid": SHA},
                               "body": "Refs #602\n\nCloses #603\n"})
        with mock.patch.object(release, "run", side_effect=gh):
            self.assertEqual(release.range_sources(cfg, OLD, SHA)[0],
                             [{"pr": 10, "merge_sha": SHA, "issue": 603, "reference": "closes"}])

    def test_range_rejects_source_reference_that_is_not_a_footer(self):
        cfg = config(Path("/tmp/release.json"))
        def gh(argv, *unused):
            command = " ".join(argv)
            if "/compare/" in command:
                return json.dumps({"status": "ahead", "total_commits": 1, "commits": [{"sha": SHA}]})
            if "/pulls" in command:
                return json.dumps([[{"number": 10, "merge_commit_sha": SHA, "merged_at": "now", "base": {"ref": "main"}}]])
            return json.dumps({"state": "MERGED", "baseRefName": "main", "mergeCommit": {"oid": SHA}, "body": "Refs #602\nMore text"})
        with mock.patch.object(release, "run", side_effect=gh), self.assertRaisesRegex(release.ReleaseError, "footer"):
            release.range_sources(cfg, OLD, SHA)

    def test_range_accepts_the_app_marker_after_its_footer(self):
        cfg = config(Path("/tmp/release.json"))
        marker = "<!-- dark-factory-operation:12345678-1234-1234-1234-123456789abc:" + "e" * 64 + " -->"
        def gh(argv, *unused):
            command = " ".join(argv)
            if "/compare/" in command:
                return json.dumps({"status": "ahead", "total_commits": 1, "commits": [{"sha": SHA}]})
            if "/pulls" in command:
                return json.dumps([[{"number": 10, "merge_commit_sha": SHA, "merged_at": "now", "base": {"ref": "main"}}]])
            return json.dumps({"state": "MERGED", "baseRefName": "main", "mergeCommit": {"oid": SHA}, "body": "Work\n\nRefs #602\n\n" + marker})
        with mock.patch.object(release, "run", side_effect=gh):
            self.assertEqual(release.range_sources(cfg, OLD, SHA)[0][0]["issue"], 602)

    def test_range_routes_terminal_source_footer_with_related_issue_prose(self):
        cfg = config(Path("/tmp/release.json"))
        def gh(argv, *unused):
            command = " ".join(argv)
            if "/compare/" in command:
                return json.dumps({"status": "ahead", "total_commits": 1, "commits": [{"sha": SHA}]})
            if "/pulls" in command:
                return json.dumps([[{"number": 10, "merge_commit_sha": SHA, "merged_at": "now", "base": {"ref": "main"}}]])
            return json.dumps({"state": "MERGED", "baseRefName": "main", "mergeCommit": {"oid": SHA},
                               "body": "Refs #784\n\nRefs #602\n"})
        with mock.patch.object(release, "run", side_effect=gh):
            self.assertEqual(release.range_sources(cfg, OLD, SHA)[0],
                             [{"pr": 10, "merge_sha": SHA, "issue": 602, "reference": "refs"}])

    @unittest.skipUnless(os.name == "posix", "process groups require POSIX")
    def test_timeout_terminates_the_hook_process_group(self):
        with tempfile.TemporaryDirectory() as directory:
            child_file = Path(directory) / "child.pid"
            code = "import pathlib,subprocess,sys,time; pathlib.Path(sys.argv[1]).write_text(str(subprocess.Popen(['/bin/sh','-c','sleep 30']).pid)); time.sleep(30)"
            with self.assertRaisesRegex(release.ReleaseError, "timed out"):
                release.run([sys.executable, "-c", code, str(child_file)], timeout=1)
            child = int(child_file.read_text())
            for _ in range(20):
                try:
                    os.kill(child, 0)
                except ProcessLookupError:
                    break
                time.sleep(0.05)
            else:
                self.fail("timed-out hook left its child running")

    def test_failed_hook_keeps_stage_exit_and_redacted_bounded_stderr(self):
        code = "import sys; sys.stderr.write('Authorization: Basic hunter3 hunter4\\nbearer hunter5 token=hunter2 ghs_abc123 in " + str(Path.home()) + "/private\\x1b[0m\\nstage: reinstall-service.sh --install-prepared exit=1\\n'); sys.exit(7)"
        with self.assertRaises(release.ReleaseError) as raised:
            release.run([sys.executable, "-c", code])
        # Short enough that only redaction, never the cut, can hide a value.
        self.assertEqual("command failed: " + Path(sys.executable).name + " -c exit=7: Authorization: *** bearer *** token=*** *** in ~/private [0m "
                         "stage: reinstall-service.sh --install-prepared exit=1", str(raised.exception))
        # A value crossing the cut keeps its label only if redaction runs first.
        self.assertEqual("token=*** stage: deploy exit=1", release.failure_tail("token=" + "h" * 3000 + "\nstage: deploy exit=1\n"))
        self.assertEqual(2000, len(release.failure_tail("e" * 5000)))

    def test_nonancestor_range_requires_explicit_baseline_setting(self):
        cfg = config(Path("/tmp/release.json"))
        with mock.patch.object(release, "run", return_value=json.dumps({"status": "diverged"})), self.assertRaisesRegex(release.ReleaseError, "not an ancestor"):
            release.range_sources(cfg, OLD, SHA)
        cfg["allow_nonancestor_baseline"] = True
        with mock.patch.object(release, "run", return_value=json.dumps({"status": "diverged"})):
            self.assertEqual(release.range_sources(cfg, OLD, SHA), ([], "nonancestor_baseline"))

    def test_first_unhealthy_probe_persists_range_before_deploy(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            cfg = config(journal)
            observed = {"sha": OLD, "healthy": False}
            verified = {"sha": SHA, "healthy": True}
            def command(argv, *unused, **kwargs):
                if argv[0] == "/bin/true":
                    self.assertIsNone(kwargs["timeout"])
                    receipt = release.load(journal)["releases"]["633"]
                    self.assertEqual(receipt["delivery_sources"], [{"pr": 633, "merge_sha": SHA, "issue": 602, "reference": "refs"}])
                    self.assertEqual(release.load(journal)["live_tip"]["sha"], OLD)
                    return ""
                if argv[:2] == ["gh", "api"]:
                    return json.dumps({"object": {"sha": SHA}})
                self.fail(str(argv))
            with mock.patch.object(release, "gh_snapshot", return_value=snapshot()), \
                 mock.patch.object(release, "review_gate"), \
                 mock.patch.object(release, "probe", return_value=observed), \
                 mock.patch.object(release, "range_sources", return_value=([{"pr": 633, "merge_sha": SHA, "issue": 602, "reference": "refs"}], "range")), \
                 mock.patch.object(release, "verify", return_value=verified), \
                 mock.patch.object(release, "run", side_effect=command):
                result = release.once(cfg, 633)
            self.assertEqual(result["state"], "verified")
            self.assertEqual(release.load(journal)["live_tip"]["sha"], SHA)


if __name__ == "__main__":
    unittest.main()
