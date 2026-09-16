#!/usr/bin/env python3
"""Run one independent host review and wake its overseer with the App receipt."""
import argparse
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import uuid


HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("factory_intake", HERE / "factory-intake.py")
intake = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(intake)
SHA = re.compile(r"^[0-9a-f]{40}$")
FOOTER = re.compile(r"(?im)^(?:refs|closes)\s+#([1-9][0-9]*)\s*$")


class ReviewError(Exception):
    pass


def mirror(config):
    root = config.get("review_mirror_root")
    if not isinstance(root, str) or not os.path.isabs(root):
        raise ReviewError("review_mirror_root must be an absolute path")
    owner, name = config["repository"].split("/", 1)
    path = Path(root) / owner / name
    if not path.is_dir() or not (path / "HEAD").is_file():
        raise ReviewError("review mirror is missing")
    if intake.command(["git", "-C", str(path), "rev-parse", "--is-bare-repository"]).strip() != "true":
        raise ReviewError("review mirror must be bare")
    origin = intake.command(["git", "-C", str(path), "remote", "get-url", "origin"]).strip()
    allowed = "https://github.com/" + config["repository"]
    if origin.removesuffix(".git") != allowed:
        raise ReviewError("review mirror origin must be the configured https GitHub repository")
    return path


def linked_issue(body, journal, repository):
    if not isinstance(body, str):
        raise ReviewError("pull request body is invalid")
    numbers = {int(value) for value in FOOTER.findall(body)}
    known = {record.get("number") for record in journal["issues"].values() if isinstance(record, dict) and record.get("managed")}
    matched = numbers & known
    if len(matched) > 1:
        raise ReviewError("pull request links multiple tracked source issues")
    return next(iter(matched), None)


def list_prs(config):
    limit = int(config.get("max_issues", 25))
    raw = intake.command(["gh", "pr", "list", "--repo", config["repository"], "--state", "open", "--limit", str(limit + 1), "--json", "number,headRefOid,body"], timeout=int(config.get("command_timeout", 30)))
    try:
        values = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ReviewError("gh returned invalid pull request JSON") from exc
    if not isinstance(values, list) or len(values) > limit:
        raise ReviewError("pull request review cap reached")
    for value in values:
        if not isinstance(value, dict) or not isinstance(value.get("number"), int) or value["number"] < 1 or not isinstance(value.get("body"), str) or not isinstance(value.get("headRefOid"), str) or not SHA.fullmatch(value["headRefOid"]):
            raise ReviewError("gh returned an invalid pull request")
    return values


def ready(config, path, pr, issue):
    base = config.get("base", "main")
    if not isinstance(base, str) or not re.fullmatch(r"[A-Za-z0-9_.-]{1,240}", base):
        raise ReviewError("base must be an explicit branch name")
    intake.command(["git", "-C", str(path), "fetch", "--no-tags", "origin", "+refs/heads/" + base + ":refs/remotes/origin/" + base, "+refs/pull/" + str(pr["number"]) + "/head:refs/pull/" + str(pr["number"]) + "/head"], timeout=120)
    head = intake.command(["git", "-C", str(path), "rev-parse", "refs/pull/" + str(pr["number"]) + "/head"]).strip()
    observed_base = intake.command(["git", "-C", str(path), "rev-parse", "refs/remotes/origin/" + base]).strip()
    if head != pr["headRefOid"] or not SHA.fullmatch(observed_base):
        raise ReviewError("mirror did not prove the App-reported exact head and base")
    marker = intake.source_marker(config, {"number": issue})
    return {"pr": pr["number"], "head": head, "base": observed_base, "source_marker": marker,
            "priority": int(config.get("priority_default", 0))}



def verify_existing(path, pr, operation):
    head, base = operation.get("head"), operation.get("base")
    if not isinstance(head, str) or not isinstance(base, str) or not SHA.fullmatch(head) or not SHA.fullmatch(base):
        raise ReviewError("review receipt is invalid")
    intake.command(["git", "-C", str(path), "fetch", "--no-tags", "origin", "+refs/pull/" + str(pr["number"]) + "/head:refs/pull/" + str(pr["number"]) + "/head"], timeout=120)
    observed = intake.command(["git", "-C", str(path), "rev-parse", "refs/pull/" + str(pr["number"]) + "/head"]).strip()
    if observed != pr["headRefOid"] or observed != head:
        raise ReviewError("mirror did not prove the App-reported exact head")
    intake.command(["git", "-C", str(path), "cat-file", "-e", base + "^{commit}"])


def observe_review(operation):
    bridge = os.environ.get("DARK_FACTORY_MAINTAINER_BRIDGE") or shutil.which("dark-factory-maintainer-mcp-bridge")
    if not bridge or not os.path.isabs(bridge):
        raise ReviewError("maintainer bridge is unavailable")
    metadata = Path(bridge).stat()
    if not stat.S_ISREG(metadata.st_mode) or not metadata.st_mode & stat.S_IXUSR or metadata.st_mode & 0o022:
        raise ReviewError("maintainer bridge is not a safe executable")
    request = {"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {
        "name": "observe_operation", "arguments": {"operation_id": operation["review_operation"]}}}
    try:
        response = subprocess.run([bridge], input=json.dumps(request) + "\n", capture_output=True, text=True, timeout=40, check=True)
        reply = json.loads(response.stdout)
        value = reply["result"]["structuredContent"]
    except (OSError, subprocess.SubprocessError, ValueError, KeyError, TypeError) as exc:
        raise ReviewError("review operation observation unavailable") from exc
    if reply.get("id") != 1 or reply["result"].get("isError") or not isinstance(value, dict) or value.get("operation_id") != operation["review_operation"]:
        raise ReviewError("review operation observation invalid")
    state = value.get("state")
    if state == "completed":
        result = value.get("result")
        if value.get("kind") != "submit_pull_request_review" or not isinstance(result, dict) or result.get("head_sha") != operation["head"] or result.get("verdict") not in {"allow", "block"}:
            raise ReviewError("review receipt does not match the exact head")
        return result["verdict"]
    if state not in {"missing", "planned", "executing", "indeterminate"}:
        raise ReviewError("review operation state invalid")
    return state


def launch_review(config, path, pr, operation):
    # The existing process-group wrapper owns and verifies reviewer cleanup.
    # A parent subprocess timeout must not kill only the shell and orphan Codex.
    directory = Path(config["journal"]).parent / ("review-" + str(pr["number"]) + "-" + operation["head"])
    directory.mkdir(mode=0o700, exist_ok=True)
    body = directory / "body.md"
    body.write_text(pr["body"])
    env = dict(os.environ, DARK_FACTORY_REVIEW_OPERATION_ID=operation["review_operation"],
               DARK_FACTORY_REVIEW_REMOTE="file://" + str(path.parent.parent))
    env.pop("DARK_FACTORY_REVIEW_EVIDENCE_FILE", None)
    with (directory / "launch.log").open("w") as output:
        return subprocess.run(["/bin/sh", "-c", '. "$1"; shift; go_gate_run_bounded "$@"', "review-process-owner",
                               str(HERE / "go-gate-environment.sh"), "1200", str(HERE / "cold-review.sh"),
                               config["repository"], str(pr["number"]), operation["head"], operation["base"], str(body)],
                              cwd=directory, env=env, stdout=output, stderr=subprocess.STDOUT).returncode


def review_followup(config, operation, state):
    task = {"priority": operation["priority"], "title": "Resume publication review for GitHub PR #" + str(operation["pr"])}
    task["task_id"] = intake.sha_id("review-result", config["project_id"], config["repository"], str(operation["pr"]), operation["head"], state)
    task["incarnation_id"] = intake.sha_id("incarnation", task["task_id"])
    task["body"] = ("Resume publication for " + operation["source_marker"] + ". Host independent review for PR #" + str(operation["pr"]) +
                    " at exact head " + operation["head"] + " and base " + operation["base"] +
                    " has App operation " + operation["review_operation"] + " with state " + state + ". Host review exit: " + str(operation.get("review_exit", "not launched")) + ". " +
                    ("Read that exact operation and its GitHub review; route blocking findings to the original task, or resume protected enqueue after ALLOW. " if state in {"allow", "block"} else
                     "The launch or submission is unresolved. Observe this operation; do not start another reviewer or invent a verdict. Report the concrete infrastructure blocker. ") +
                    "Never submit your own verdict or run a nested cold-review. Merge and deployment remain separate delivery gates.")
    return task


def config_fingerprint(config):
    value = {key: config.get(key) for key in ("repository", "project_id", "overseer_agent_id", "review_mirror_root")}
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def run_once(config):
    config = intake.validate_config(config)
    path = mirror(config)
    journal_path = Path(config["journal"] + ".reviews.json")
    lock_path = Path(str(journal_path) + ".lock")
    journal = intake.load_journal(Path(config["journal"]))
    intake.bind_journal(config, journal)
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    with lock_path.open("a+") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise ReviewError("another review intake process owns the journal") from exc
        return run_locked(config, path, journal, journal_path)


def run_locked(config, path, journal, journal_path):
    if journal_path.exists():
        try:
            receipts = json.loads(journal_path.read_text())
        except (OSError, json.JSONDecodeError) as exc:
            raise ReviewError("review receipt is unreadable") from exc
        if receipts.get("version") != 2 or not isinstance(receipts.get("pulls"), dict) or receipts.get("config_fingerprint") != config_fingerprint(config):
            raise ReviewError("review receipt is invalid")
    else:
        receipts = {"version": 2, "config_fingerprint": config_fingerprint(config), "pulls": {}}
    messages = []
    launched = False
    for pr in list_prs(config):
        issue = linked_issue(pr["body"], journal, config["repository"])
        if issue is None:
            continue
        key = str(pr["number"]) + ":" + pr["headRefOid"]
        existing = receipts["pulls"].get(key)
        if existing is None:
            operation = ready(config, path, pr, issue)
            receipts["pulls"][key] = operation
            intake.atomic_json(journal_path, receipts)
        else:
            operation = existing
            verify_existing(path, pr, operation)
        operation.setdefault("review_operation", str(uuid.uuid5(uuid.NAMESPACE_URL, "dark-factory:host-review:" + config["repository"] + ":" + str(pr["number"]) + ":" + operation["head"])))
        state = observe_review(operation)
        if state == "missing" and not operation.get("review_attempted"):
            if launched:
                continue
            # Persist before launching: a crash cannot authorize a second model
            # run while the first may still be submitting its exact-head verdict.
            operation["review_attempted"] = True
            intake.atomic_json(journal_path, receipts)
            launched = True
            operation["review_exit"] = launch_review(config, path, pr, operation)
            intake.atomic_json(journal_path, receipts)
            state = observe_review(operation)
        if state not in {"allow", "block"}:
            state = "unresolved"
        operation["review_state"] = state
        intake.atomic_json(journal_path, receipts)
        followup = review_followup(config, operation, state)
        if intake.task_state(config, followup) is None:
            intake.enqueue(config, followup)
            messages.append("woke PR #" + str(pr["number"]) + " review " + state)
    return messages


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("config", type=Path)
    parser.add_argument("--once", action="store_true")
    args = parser.parse_args(argv)
    try:
        config = json.loads(args.config.read_text())
        print(json.dumps({"ok": True, "messages": run_once(config)}))
        return 0
    except (OSError, ValueError, json.JSONDecodeError, intake.IntakeError, ReviewError) as exc:
        print("factory-review-intake: " + str(exc), file=__import__("sys").stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
