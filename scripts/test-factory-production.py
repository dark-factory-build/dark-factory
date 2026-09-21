#!/usr/bin/env python3
"""Offline facts-only fixtures for the production observation collector."""
import importlib.util
import json
import subprocess
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
    def setUp(self):
        pass

    def test_collector_leaves_pull_request_authority_to_the_daemon(self):
        with mock.patch.object(production, "host_config", return_value={"repository": "o/r", "factory_home": "/tmp/factory", "journal": "/tmp/journal", "release_configs": []}), mock.patch.object(production, "journal_paths", return_value=([], [], False)), mock.patch.object(production, "maintenance", return_value={"state": "unknown"}), mock.patch.object(production.time, "time", return_value=10):
            result = production.collect({"repository": "o/r"})
        self.assertEqual(result["observed_at"], 10000)
        self.assertEqual(result["pull_requests"], [])
        self.assertEqual(result["checks"], [])
        self.assertEqual(result["reviewers"], [])

    def test_release_membership_preserves_source_less_prs_and_reports_overflow(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            journal.write_text(json.dumps({"version": 1, "releases": {"one": {
                "sha": SHA, "state": "verified", "verified_at": 9,
                "verification": {"healthy": True, "sha": SHA},
                "delivery_sources": [{"pr": 999}],
                "included_pull_requests": [{"pr": n, "merge_sha": SHA} for n in range(1, 258)],
            }}}))
            deliveries, unavailable, _ = production.release_receipts([
                {"path": str(journal), "repository": "o/r", "destination": "site:example"}
            ], "o/r")
            self.assertFalse(unavailable)
            self.assertEqual(len(deliveries), 1)
            self.assertEqual(deliveries[0]["pull_requests"], list(range(1, 257)))
            self.assertEqual(deliveries[0]["overflow"], 1)

    def test_release_metadata_comes_from_latest_receipt_while_membership_is_union(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            journal.write_text(json.dumps({"version": 1, "releases": {
                "old": {"sha": SHA, "state": "blocked", "phase": "deploying", "error": "old failure",
                         "updated_at": 10, "delivery_sources": [{"pr": 7}]},
                "new": {"sha": SHA, "state": "verified", "phase": "verified", "updated_at": 20,
                         "verified_at": 21, "verification": {"healthy": True, "sha": SHA, "url": "https://new.example"},
                         "included_pull_requests": [{"pr": 8}]},
            }}))
            deliveries, unavailable, _ = production.release_receipts([
                {"path": str(journal), "repository": "o/r", "destination": "site:example"}
            ], "o/r")
            self.assertFalse(unavailable)
            self.assertEqual(deliveries[0]["state"], "verified")
            self.assertEqual(deliveries[0]["phase"], "verified")
            self.assertEqual(deliveries[0]["reason"], "")
            self.assertEqual(deliveries[0]["updated_at"], 20000)
            self.assertEqual(deliveries[0]["pull_requests"], [7, 8])
            self.assertEqual(deliveries[0]["url"], "https://new.example")

    def test_newer_failed_receipt_replaces_older_verified_metadata_and_keeps_own_pr(self):
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            journal.write_text(json.dumps({"version": 1, "releases": {
                "old": {"pr": 958, "sha": SHA, "state": "verified", "verified_at": 10,
                         "verification": {"healthy": True, "sha": SHA, "url": "https://old.example"},
                         "included_pull_requests": [{"pr": 959}]},
                "new": {"pr": 959, "sha": SHA, "state": "blocked", "phase": "deploying",
                         "error": "new failure", "updated_at": 20},
            }}))
            deliveries, unavailable, _ = production.release_receipts([
                {"path": str(journal), "repository": "o/r", "destination": "site:example"}
            ], "o/r")
            self.assertFalse(unavailable)
            self.assertEqual(deliveries[0]["state"], "blocked")
            self.assertEqual(deliveries[0]["phase"], "deploying")
            self.assertEqual(deliveries[0]["reason"], "new failure")
            self.assertNotIn("url", deliveries[0])
            self.assertEqual(deliveries[0]["pull_requests"], [958, 959])

    def test_tied_same_revision_receipts_keep_unresolved_evidence_in_both_orders(self):
        verified = {"pr": 7, "sha": SHA, "state": "verified", "updated_at": 20,
                    "verified_at": 20, "verification": {"healthy": True, "sha": SHA}}
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "release.json"
            for state in ("blocked", "running", "unknown"):
                unresolved = {"pr": 8, "sha": SHA, "state": state, "updated_at": 20}
                for receipts in ([unresolved, verified], [verified, unresolved]):
                    journal.write_text(json.dumps({"version": 1, "releases": dict(enumerate(receipts))}))
                    deliveries, unavailable, _ = production.release_receipts([
                        {"path": str(journal), "repository": "o/r", "destination": "site:example"}
                    ], "o/r")
                    self.assertFalse(unavailable)
                    self.assertEqual(len(deliveries), 1)
                    self.assertEqual(deliveries[0]["state"], state)
                    self.assertNotIn("verified_at", deliveries[0])
                    self.assertEqual(sorted(deliveries[0]["pull_requests"]), [7, 8])

    def test_customer_config_has_no_github_or_credential_fallback(self):
        with mock.patch.object(production.intake, "command") as command:
            result = production.collect({"repository": "o/r"})
        self.assertEqual(result["unavailable"], "host_controller_only")
        command.assert_not_called()

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

    def test_maintenance_requires_coherent_release_identity_and_trusted_status(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory) / "factory"
            binary_root = Path(str(home) + ".service") / "bin" / "current"
            binary_root.mkdir(parents=True)
            for name in ("factoryctl", "factoryd", "factory-runner"):
                (binary_root / name).write_text("")
            config = {"repository": "dark-factory-build/dark-factory", "factory_home": str(home)}
            identity = {"version": "1.2.3", "source": SHA, "target": "darwin/arm64", "build_id": "build", "release": True}
            def command(argv, **kwargs):
                if argv[-1] == "--build-identity":
                    self.assertEqual(kwargs["env"], {})
                    return subprocess.CompletedProcess(argv, 0, json.dumps(identity), "")
                self.assertEqual(argv[-2:], ["web", "status"])
                resolved = home.resolve()
                self.assertEqual(kwargs["env"], {"DARK_FACTORY_SOCKET": str(resolved / "runtimes" / "factory.sock"),
                                                  "DARK_FACTORY_OPERATOR_TOKEN_FILE": str(resolved / "operator.token")})
                status = dict(identity, **{"ready": True})
                return subprocess.CompletedProcess(argv, 0, json.dumps({"ready": True, "build": status}), "")
            with mock.patch.object(production, "github", return_value={"tag_name": "v1.2.3", "html_url": "https://github.com/dark-factory-build/dark-factory/releases/tag/v1.2.3"}), \
                 mock.patch.object(production.subprocess, "run", side_effect=command):
                result = production.maintenance(config)
            self.assertEqual(result["state"], "ready")
            self.assertEqual(result["destination"], production.runtime_destination(home))
            self.assertTrue(result["destination"].startswith("runtime:host-"))
            self.assertNotIn(str(home.resolve()), result["destination"])
            self.assertEqual(result["available"]["state"], "available")
            self.assertEqual(result["installed"]["state"], "verified")
            self.assertEqual(result["running"]["state"], "ready")
            self.assertEqual(result["running"]["build_id"], "build")

    def test_runtime_release_destination_is_stable_and_opaque(self):
        home = Path("/private/tmp/factory-release-home")
        config = {"factory_home": str(home)}
        release = {"verify_argv": ["/tools/verify-live-runtime.py"]}
        destination = production.release_destination(config, release)
        self.assertEqual(destination, production.runtime_destination(home))
        self.assertTrue(destination.startswith("runtime:host-"))
        self.assertNotIn(str(home.resolve()), destination)


if __name__ == "__main__":
    unittest.main()
