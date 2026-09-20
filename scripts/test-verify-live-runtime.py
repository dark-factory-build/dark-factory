#!/usr/bin/env python3
import importlib.util
import pathlib
import unittest


path = pathlib.Path(__file__).with_name("verify-live-runtime.py")
spec = importlib.util.spec_from_file_location("verify_live_runtime", path)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class VerifyLiveRuntimeTest(unittest.TestCase):
    def test_old_running_revision_is_valid_before_requested_upgrade(self):
        old = "a" * 40
        status = {"ready": True, "build": {"source": old, "release": True}}
        self.assertTrue(module.runtime_status_matches_revision(status, old))

    def test_running_revision_must_match_installed_revision(self):
        old, other = "a" * 40, "b" * 40
        status = {"ready": True, "build": {"source": other, "release": True}}
        self.assertFalse(module.runtime_status_matches_revision(status, old))


if __name__ == "__main__":
    unittest.main()
