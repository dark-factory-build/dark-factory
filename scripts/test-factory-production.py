#!/usr/bin/env python3
"""Offline facts-only fixtures for the production observation collector."""
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock


MODULE = Path(__file__).with_name("factory-production.py")
SPEC = importlib.util.spec_from_file_location("factory_production", MODULE)
production = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(production)
SHA = "a" * 40
OLD = "b" * 40


class ProductionFixtures(unittest.TestCase):
    def test_collector_links_only_real_heads_or_run_pull_request_membership(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            journal, reviews, releases, release_config = root / "intake.json", root / "intake.json.reviews.json", root / "releases.json", root / "release.json"
            reviews.write_text(json.dumps({"version": 2, "pulls": {
                "7:" + SHA: {"pr": 7, "head": SHA, "review_operation": "exact", "review_state": "allow", "review_exit": 0, "merge_state": "QUEUED", "provider": "codex"},
                "7:" + OLD: {"pr": 7, "head": OLD, "review_operation": "old", "review_attempted": True},
            }}))
            releases.write_text(json.dumps({"version": 1, "releases": {
                "7": {"pr": 7, "sha": SHA, "state": "verified", "verified_at": 9,
                      "verification": {"sha": SHA, "healthy": True, "deployment_id": "deploy-1", "url": "https://deploy.example/1"},
                      "delivery_sources": [{"pr": 7}, {"pr": 8}, {"pr": 9, "repository": "elsewhere/repo"}]}
            }}))
            release_config.write_text(json.dumps({"repository": "o/r", "journal": str(releases), "verify_argv": ["/tools/verify-live-site.py"]}))
            calls = []
            def command(argv, **unused):
                calls.append(argv)
                endpoint = argv[2]
                if endpoint.endswith("/pulls?state=open&per_page=100"):
                    return json.dumps([{"number": 7, "title": "Real", "html_url": "https://github.com/o/r/pull/7", "state": "open", "merge_commit_sha": OLD, "head": {"sha": SHA, "ref": "real"}, "base": {"ref": "main"}}])
                if endpoint.endswith("/pulls?state=closed&sort=updated&direction=desc&per_page=100"):
                    return json.dumps([{"number": 8, "title": "Merged", "html_url": "https://github.com/o/r/pull/8", "state": "closed", "merged_at": "2026-09-20T10:00:00Z", "merge_commit_sha": OLD, "head": {"sha": OLD, "ref": "old"}, "base": {"ref": "main"}}])
                if endpoint.endswith("/actions/runs?per_page=100"):
                    return json.dumps({"workflow_runs": [
                        {"id": 1, "name": "shared", "head_sha": SHA, "status": "completed", "conclusion": "success", "event": "push", "html_url": "https://github.com/o/r/actions/runs/1", "pull_requests": []},
                        {"id": 1, "name": "duplicate", "head_sha": SHA, "pull_requests": [{"number": 7}]},
                        {"id": 2, "name": "old real membership", "head_sha": OLD, "status": "completed", "conclusion": "failure", "event": "merge_group", "html_url": "https://github.com/o/r/actions/runs/2", "pull_requests": [{"number": 7}]},
                        {"id": 3, "name": "branch-looking but unrelated", "head_sha": "c" * 40, "pull_requests": []},
                    ]})
                if "/actions/runs/" in endpoint:
                    return json.dumps({"total_count": 33, "jobs": [{"id": 11, "name": "x" * 300, "status": "completed", "conclusion": "success", "html_url": "https://github.com/o/r/actions/jobs/11"}]})
                self.fail(endpoint)
            config = {"repository": "o/r", "project_id": "1" * 32, "overseer_agent_id": "2" * 32, "label": "factory:ready", "allowed_authors": ["owner"], "factory_home": str(root), "journal": str(journal), "release_configs": [str(release_config)]}
            with mock.patch.object(production, "host_config", return_value=config), mock.patch.object(production.intake, "command", side_effect=command), mock.patch.object(production.time, "time", return_value=10):
                result = production.collect(config)
            self.assertEqual(result["pull_requests"][0]["review"], {"head": SHA, "state": "allow"})
            self.assertEqual(result["pull_requests"][0]["merge_queue"], "QUEUED")
            self.assertEqual(result["observed_at"], 10000)
            self.assertEqual(result["pull_requests"][1]["merge"], OLD)
            self.assertEqual(result["pull_requests"][1]["merged_at"], "2026-09-20T10:00:00Z")
            self.assertEqual([(item["id"], item["scope"], item["pull_requests"], item.get("overflow")) for item in result["checks"]], [("1", "head", [7], 1), ("2", "merge_group", [7, 8], 1)])
            self.assertEqual(len(result["checks"][0]["jobs"][0]["name"]), 256)
            self.assertEqual(result["reviewers"][-1]["state"], "unknown")
            self.assertNotIn("merge", result["pull_requests"][0])
            self.assertEqual(result["deliveries"], [{"id": "site:app.darkfactory.build:release:" + SHA, "kind": "release", "destination": "site:app.darkfactory.build", "revision": SHA, "state": "verified", "url": "https://deploy.example/1", "pull_requests": [7, 8], "verified_at": 9000}])
            self.assertEqual(len([call for call in calls if "/actions/runs/1/jobs" in call[2]]), 1)

    def test_customer_config_has_no_github_or_credential_fallback(self):
        with mock.patch.object(production.intake, "command") as command:
            result = production.collect({"repository": "o/r"})
        self.assertEqual(result["unavailable"], "host_controller_only")
        command.assert_not_called()

    def test_running_reviewer_requires_matching_live_sidecar_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "intake.json"
            review_path = Path(str(journal) + ".reviews.json")
            operation = {"pr": 7, "head": SHA, "review_operation": "review-7", "review_attempted": True}
            review_path.write_text(json.dumps({"version": 2, "pulls": {"7:" + SHA: operation}}))
            activity = review_path.parent / ("review-7-" + SHA) / "activity.json"
            activity.parent.mkdir()
            activity.write_text(json.dumps({"operation": "review-7", "pr": 7, "head": SHA, "repository": "o/r", "pid": 99, "process_start": "Sat Sep 20 12:00:00 2026", "started_at": 1000}))
            entry = {"path": str(review_path), "repository": "o/r"}
            with mock.patch.object(production, "process_start", return_value="Sat Sep 20 12:00:00 2026"):
                reviewers, _, _, unavailable, _ = production.review_receipts([entry], "o/r")
            self.assertFalse(unavailable)
            self.assertEqual(reviewers[0]["state"], "running")
            activity.write_text(json.dumps({"operation": "wrong", "pr": 7, "head": SHA, "repository": "o/r", "pid": 99, "process_start": "Sat Sep 20 12:00:00 2026"}))
            with mock.patch.object(production, "process_start", return_value="Sat Sep 20 12:00:00 2026"):
                reviewers, _, _, _, _ = production.review_receipts([entry], "o/r")
            self.assertEqual(reviewers[0]["state"], "unknown")

    def test_full_pr_pages_and_actions_total_count_are_explicit_overflow(self):
        config = {"repository": "o/r", "project_id": "1" * 32, "overseer_agent_id": "2" * 32,
                  "label": "factory:ready", "allowed_authors": ["owner"], "factory_home": "/tmp/factory",
                  "journal": "/tmp/journal", "release_configs": []}
        pr = {"number": 7, "title": "Real", "html_url": "https://github.com/o/r/pull/7", "state": "open", "head": {"sha": SHA, "ref": "real"}, "base": {"ref": "main"}}
        def command(argv, **unused):
            if "/pulls?state=" in argv[2]:
                return json.dumps([pr] * 100)
            if "/actions/runs?" in argv[2]:
                return json.dumps({"total_count": 101, "workflow_runs": []})
            self.fail(argv)
        with mock.patch.object(production, "host_config", return_value=config), mock.patch.object(production.intake, "command", side_effect=command):
            result = production.collect(config)
        self.assertEqual(result["overflow"], 3)
        self.assertLessEqual(len(result["pull_requests"]), 256)

    def test_record_uses_only_service_socket_and_token_file_environment(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory) / "home"
            factoryctl = Path(str(home) + ".service") / "bin" / "current" / "factoryctl"
            factoryctl.parent.mkdir(parents=True)
            factoryctl.write_text("")
            config = {"factory_home": str(home), "project_id": "1" * 32}
            with mock.patch.object(production, "host_config", return_value=config), mock.patch.object(production.subprocess, "run", return_value=mock.Mock(returncode=0)) as run:
                self.assertTrue(production.record(config, {"repository": "o/r"}))
            self.assertEqual(run.call_args.args[0], [str(factoryctl), "production", "observe", "--json-stdin"])
            self.assertEqual(run.call_args.kwargs["env"], {"DARK_FACTORY_SOCKET": str(home / "runtimes" / "factory.sock"), "DARK_FACTORY_OPERATOR_TOKEN_FILE": str(home / "operator.token")})

    def test_record_chunks_large_observations_without_losing_health_facts(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory) / "home"
            factoryctl = Path(str(home) + ".service") / "bin" / "current" / "factoryctl"
            factoryctl.parent.mkdir(parents=True)
            factoryctl.write_text("")
            config = {"factory_home": str(home), "project_id": "1" * 32}
            observation = {"repository": "o/r", "observed_at": 1000, "unavailable": "jobs", "overflow": 3,
                           "pull_requests": [{"number": index, "title": "x" * 1000} for index in range(1, 257)], "checks": [], "reviewers": [], "deliveries": []}
            with mock.patch.object(production, "host_config", return_value=config), mock.patch.object(production.subprocess, "run", return_value=mock.Mock(returncode=0)) as run:
                self.assertTrue(production.record(config, observation))
            self.assertGreater(run.call_count, 1)
            for call in run.call_args_list:
                document = json.loads(call.kwargs["input"])
                self.assertLessEqual(len(call.kwargs["input"].encode()), production.MAX_INPUT)
                self.assertEqual({"repository": "o/r", "observed_at": 1000, "unavailable": "jobs", "overflow": 3}, {key: document["observation"][key] for key in ("repository", "observed_at", "unavailable", "overflow")})


if __name__ == "__main__":
    unittest.main()
