#!/usr/bin/env python3
"""Compatibility bridge for controllers not yet migrated to the product API."""
import json
import os
from pathlib import Path
import subprocess


def deliver(intake_config, release_config, receipt):
    home = Path(intake_config["factory_home"])
    request = {
        "project_id": intake_config["project_id"],
        "repository": intake_config["repository"],
        "overseer_agent_id": intake_config["overseer_agent_id"],
        "priority_default": int(intake_config.get("priority_default", 0)),
        "release": {"repository": release_config["repository"]},
        "receipt": receipt,
    }
    environment = os.environ.copy()
    environment["DARK_FACTORY_SOCKET"] = str(home / "runtimes" / "factory.sock")
    environment["DARK_FACTORY_OPERATOR_TOKEN_FILE"] = str(home / "operator.token")
    executable = intake_config.get("factoryctl", "factoryctl")
    completed = subprocess.run([executable, "delivery", "reconcile", "--json-stdin"], input=json.dumps(request), text=True, capture_output=True, check=True, env=environment)
    result = json.loads(completed.stdout)
    return result["task_ids"]
