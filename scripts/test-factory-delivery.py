#!/usr/bin/env python3
"""Offline tests for source-linked deployment follow-ups."""
import importlib.util
import unittest
from pathlib import Path
from unittest import mock


MODULE = Path(__file__).with_name("factory-delivery.py")
SPEC = importlib.util.spec_from_file_location("factory_delivery", MODULE)
delivery = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(delivery)
SHA = "a" * 40


class TaskStateIdentity(unittest.TestCase):
    def test_found_task_owned_by_another_project_or_overseer_is_a_conflict(self):
        config = {"project_id": "1" * 32, "overseer_agent_id": "2" * 32}
        operation = {"task_id": "3" * 32, "incarnation_id": "4" * 32}
        owned = {"state": "found", "task_id": operation["task_id"], "incarnation_id": operation["incarnation_id"], "project_id": config["project_id"], "assigned_agent_id": config["overseer_agent_id"]}
        with mock.patch.object(delivery, "factoryctl", return_value=owned):
            self.assertEqual(owned, delivery.task_state(config, operation))
        for key, value in (("project_id", "9" * 32), ("assigned_agent_id", "9" * 32), ("task_id", "9" * 32), ("incarnation_id", "9" * 32)):
            with mock.patch.object(delivery, "factoryctl", return_value=dict(owned, **{key: value})), self.assertRaises(ValueError):
                delivery.task_state(config, operation)


class DeliveryFixtures(unittest.TestCase):
    def setUp(self):
        self.enqueued = []
        for name, value in (("task_state", lambda config, operation: None), ("enqueue", lambda config, operation: self.enqueued.append(operation))):
            patcher = mock.patch.object(delivery, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        self.config = {"repository": "example/factory", "project_id": "1" * 32, "overseer_agent_id": "2" * 32, "priority_default": 0}
        self.receipt = {"state": "verified", "sha": SHA, "pr": 11,
                        "verification": {"sha": SHA, "healthy": True}, "delivery_mode": "range",
                        "delivery_sources": [{"pr": 10, "merge_sha": SHA, "issue": 602, "reference": "refs"},
                                             {"pr": 11, "merge_sha": SHA, "issue": 512, "reference": "closes"}]}

    def test_enqueues_one_idempotent_follow_up_per_source_mapping(self):
        task_ids = delivery.deliver(self.config, {"repository": "example/factory"}, self.receipt)
        self.assertEqual(len(task_ids), 2)
        self.assertEqual(len(self.enqueued), 2)
        self.assertIn("issue #602", self.enqueued[0]["body"])
        self.assertIn("issue #512", self.enqueued[1]["body"])

    def test_shared_source_is_never_closed_by_destination_delivery(self):
        self.receipt['delivery_sources'][0]['repository'] = 'other/backlog'
        task_ids = delivery.deliver(self.config, {"repository": "example/factory"}, self.receipt)
        self.assertEqual(len(task_ids), 1)
        self.assertNotIn('602', self.enqueued[0]['body'])

    def test_legacy_receipt_without_mapping_is_rejected(self):
        del self.receipt["delivery_sources"]
        with self.assertRaisesRegex(ValueError, "source mapping"):
            delivery.deliver(self.config, {"repository": "example/factory"}, self.receipt)

    def test_unchanged_reverification_keeps_membership_without_a_second_sweep(self):
        # The release journal retains delivery_sources across re-verification
        # of the same tip; that is not a baseline error and enqueues nothing.
        self.receipt.update({"delivery_mode": "unchanged"})
        self.assertEqual(delivery.deliver(self.config, {"repository": "example/factory"}, self.receipt), [])
        self.assertEqual(self.enqueued, [])

    def test_baseline_has_no_delivery_sweep(self):
        self.receipt.update({"delivery_mode": "baseline_current", "delivery_sources": []})
        self.assertEqual(delivery.deliver(self.config, {"repository": "example/factory"}, self.receipt), [])
        self.assertEqual(self.enqueued, [])


if __name__ == "__main__":
    unittest.main()
