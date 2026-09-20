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
            reviews, releases = root / "reviews.json", root / "releases.json"
            reviews.write_text(json.dumps({"version": 2, "pulls": {
                "7:" + SHA: {"pr": 7, "head": SHA, "review_operation": "exact", "review_state": "allow", "review_exit": 0, "merge_state": "QUEUED", "provider": "codex"},
                "7:" + OLD: {"pr": 7, "head": OLD, "review_operation": "old", "review_attempted": True},
            }}))
            releases.write_text(json.dumps({"version": 1, "releases": {
                "7": {"pr": 7, "sha": SHA, "state": "verified", "verified_at": 9,
                      "verification": {"sha": SHA, "healthy": True, "id": "deploy-1", "destination": "site"},
                      "delivery_sources": [{"pr": 7}, {"pr": 8}]}
            }}))
            calls = []
            def command(argv, **unused):
                calls.append(argv)
                endpoint = argv[2]
                if endpoint.endswith("/pulls?state=open&per_page=100"):
                    return json.dumps([{"number": 7, "title": "Real", "html_url": "https://github.com/o/r/pull/7", "state": "open", "mergeable_state": "clean", "head": {"sha": SHA, "ref": "real"}, "base": {"ref": "main"}}])
                if endpoint.endswith("/actions/runs?per_page=100"):
                    return json.dumps({"workflow_runs": [
                        {"id": 1, "name": "shared", "head_sha": SHA, "status": "completed", "conclusion": "success", "event": "push", "html_url": "u", "pull_requests": []},
                        {"id": 1, "name": "duplicate", "head_sha": SHA, "pull_requests": [{"number": 7}]},
                        {"id": 2, "name": "old real membership", "head_sha": OLD, "status": "completed", "conclusion": "failure", "event": "pull_request", "html_url": "u2", "pull_requests": [{"number": 7}]},
                        {"id": 3, "name": "branch-looking but unrelated", "head_sha": OLD, "pull_requests": []},
                    ]})
                if "/actions/runs/" in endpoint:
                    return json.dumps({"total_count": 33, "jobs": [{"id": 11, "name": "test", "status": "completed", "conclusion": "success", "html_url": "job"}]})
                self.fail(endpoint)
            config = {"repository": "o/r", "host_controller": True,
                      "observation_journals": {"reviews": [str(reviews)], "releases": [str(releases)]}}
            with mock.patch.object(production.intake, "command", side_effect=command), mock.patch.object(production.time, "time", return_value=10):
                result = production.collect(config)
            self.assertEqual(result["pull_requests"][0]["review"], {"head": SHA, "state": "allow"})
            self.assertEqual(result["pull_requests"][0]["merge"], "QUEUED")
            self.assertEqual([(item["id"], item["pull_requests"], item.get("overflow")) for item in result["checks"]], [("1", [7], 1), ("2", [7], 1)])
            self.assertEqual(result["reviewers"][-1]["state"], "unknown")
            self.assertEqual(result["deliveries"], [{"id": "deploy-1", "kind": "release", "destination": "site", "revision": SHA, "state": "verified", "pull_requests": [7, 8], "verified_at": 9}])
            self.assertEqual(len([call for call in calls if "/actions/runs/1/jobs" in call[2]]), 1)

    def test_customer_config_has_no_github_or_credential_fallback(self):
        with mock.patch.object(production.intake, "command") as command:
            result = production.collect({"repository": "o/r"})
        self.assertEqual(result["unavailable"], "host_controller_only")
        command.assert_not_called()


if __name__ == "__main__":
    unittest.main()
