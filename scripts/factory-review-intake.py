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
import time
from urllib.parse import urlparse
import uuid


HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("factory_intake", HERE / "factory-intake.py")
intake = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(intake)
PUBLICATION_SPEC = importlib.util.spec_from_file_location("factory_publication", HERE / "factory-publication.py")
publication = importlib.util.module_from_spec(PUBLICATION_SPEC)
PUBLICATION_SPEC.loader.exec_module(publication)
SHA = re.compile(r"^[0-9a-f]{40}$")


class ReviewError(Exception):
    pass


class Unproven(Exception):
    """A footer claims a source this pass cannot prove; only that PR is skipped."""


def review_provider(config):
    """Return the operator-selected installed review route.

    Codex is the durable default because it is the only current factory route
    that can obtain an exact retained-source receipt.  The explicit config
    escape hatch is for a later installed Claude source capability; ambient
    environment state must never change a queued review's route.
    """
    value = config.get("review_provider", "codex")
    if value not in {"codex", "claude"}:
        raise ReviewError("review_provider must be codex or claude")
    return value


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


def app_receipt(body, kinds, number, repository, object_path):
    """True when the body's App marker is a completed receipt that created or rewrote object `number`."""
    marker = publication.APP_MARKER_TRAILER.search(body)
    if marker is None:
        return False
    value = observe_operation(marker.group(1))
    result = value.get("result")
    if value["state"] != "completed" or value.get("kind") not in kinds or value.get("request_digest") != marker.group(2):
        return False
    if not isinstance(result, dict) or result.get("number") != number or not isinstance(result.get("url"), str):
        return False
    expected = "https://github.com/" + repository + "/" + object_path + "/" + str(number)
    return result["url"].casefold() == expected.casefold()


def app_update_receipt(body, number, repository):
    """Prove the exact current body was rendered by one completed App update."""
    marker = publication.APP_MARKER_TRAILER.search(body)
    if marker is None:
        return False
    value = observe_operation(marker.group(1))
    result = value.get("result")
    if value["state"] != "completed" or value.get("kind") != "update_pull_request_body" \
            or value.get("request_digest") != marker.group(2) or not isinstance(result, dict) \
            or result.get("number") != number or not isinstance(result.get("url"), str):
        return False
    expected = "https://github.com/" + repository + "/pull/" + str(number)
    if result["url"].casefold() != expected.casefold():
        return False
    prefix = body[:marker.start()]
    if prefix == "":
        updated_body = ""
    elif prefix.endswith("\n\n"):
        updated_body = prefix[:-2]
    else:
        return False
    request = {"repository": repository, "operation_id": marker.group(1),
               "pull_number": number, "body": updated_body}
    digest = hashlib.sha256(json.dumps(request, separators=(",", ":")).encode()).hexdigest()
    return digest == marker.group(2)


def linked_issue(config, pr, journal, existing=None):
    body = pr["body"]
    if not isinstance(body, str):
        raise ReviewError("pull request body is invalid")
    footer = publication.terminal_footer(body)
    numbers = {int(value) for repository, value in publication.FOOTER.findall(body)
               if not repository or repository.casefold() == config["repository"].casefold()}
    known = {record.get("number") for record in journal["issues"].values() if isinstance(record, dict) and record.get("managed")}
    matched = numbers & known
    if len(matched) > 1:
        raise ReviewError("pull request links multiple tracked source issues")
    if footer is None:
        return None
    if footer.group(2) and footer.group(2).casefold() != config["repository"].casefold():
        return None  # Another source controller owns this fully qualified backlog.
    issue = int(footer.group(3))
    if issue in known:
        return issue
    if matched:
        raise Unproven("terminal footer #" + str(issue) + " is not the tracked source #" + str(min(matched)) + " the body also links")
    if existing is not None and existing.get("source_marker") == intake.source_marker(config, {"number": issue}):
        return issue
    # An overseer tracking issue never enters the intake journal (it has no
    # intake label), so prove the App wrote both objects: the PR's own marker
    # is a completed publication receipt for this PR number, and the footer
    # issue's marker is the completed create_issue receipt for that number.
    if not app_receipt(body, {"create_pull_request", "update_pull_request_body"}, pr["number"], config["repository"], "pull"):
        raise Unproven("footer #" + str(issue) + " is not an intake-managed source and PR #" + str(pr["number"]) + " has no completed App publication receipt")
    try:
        source = intake.exact_issue(config, issue)
    except intake.IssueBodyTooLarge as exc:
        raise Unproven("footer #" + str(issue) + " points to a source whose body exceeds the intake limit") from exc
    if not app_receipt(source["body"], {"create_issue"}, issue, config["repository"], "issues"):
        raise Unproven("footer #" + str(issue) + " is neither an intake-managed source nor an App-created tracking issue (no completed create_issue receipt)")
    return issue


def discovery_batch_size(config):
    # GitHub's REST pull-request endpoint caps per_page at 100.
    return min(int(config.get("max_issues", 25)), 100)


def next_discovery_page(config, page, discovered):
    return 1 if discovered < discovery_batch_size(config) else page + 1


def list_prs(config, page=1):
    batch = discovery_batch_size(config)
    if type(page) is not int or page < 1:
        raise ReviewError("pull request discovery page is invalid")
    # GitHub's pull-list endpoint gives us a bounded page cursor. Rotating the
    # durable page across passes prevents entries beyond one full page from
    # being permanently starved by newer open PRs.
    raw = intake.command(["gh", "api", "repos/" + config["repository"] + "/pulls", "--method", "GET",
                          "--field", "state=open", "--field", "per_page=" + str(batch),
                          "--field", "page=" + str(page)], timeout=int(config.get("command_timeout", 30)))
    try:
        values = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ReviewError("gh returned invalid pull request JSON") from exc
    if not isinstance(values, list) or len(values) > batch:
        raise ReviewError("gh returned more pull requests than the bounded discovery batch")
    normalized = []
    for value in values:
        head = value.get("head") if isinstance(value, dict) else None
        if not isinstance(value, dict) or not isinstance(value.get("number"), int) or value["number"] < 1 \
                or not isinstance(value.get("body"), (str, type(None))) or not isinstance(head, dict) \
                or not isinstance(head.get("sha"), str) or not SHA.fullmatch(head["sha"]):
            raise ReviewError("gh returned an invalid pull request")
        normalized.append({"number": value["number"], "headRefOid": head["sha"], "body": value.get("body") or ""})
    return normalized


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


def bridge_call(name, arguments):
    bridge = os.environ.get("DARK_FACTORY_MAINTAINER_BRIDGE") or shutil.which("dark-factory-maintainer-mcp-bridge")
    if not bridge or not os.path.isabs(bridge):
        raise ReviewError("maintainer bridge is unavailable")
    metadata = Path(bridge).stat()
    if not stat.S_ISREG(metadata.st_mode) or not metadata.st_mode & stat.S_IXUSR or metadata.st_mode & 0o022:
        raise ReviewError("maintainer bridge is not a safe executable")
    request = {"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": name, "arguments": arguments}}
    try:
        response = subprocess.run([bridge], input=json.dumps(request) + "\n", capture_output=True, text=True, timeout=40, check=True)
        reply = json.loads(response.stdout)
        result = reply["result"]
    except (OSError, subprocess.SubprocessError, ValueError, KeyError, TypeError) as exc:
        raise ReviewError(name + " unavailable") from exc
    if reply.get("id") != 1 or not isinstance(result, dict):
        raise ReviewError(name + " reply invalid")
    return result


def observe_operation(operation_id):
    result = bridge_call("observe_operation", {"operation_id": operation_id})
    value = result.get("structuredContent")
    returned_id = value.get("operation_id") if isinstance(value, dict) else None
    # The App canonicalizes operation_id to lowercase before journal lookup and
    # returns that canonical form, so a persisted id that is not already
    # lowercase must still compare equal here.
    if result.get("isError") or not isinstance(returned_id, str) or returned_id.lower() != operation_id.lower():
        raise ReviewError("operation observation invalid")
    state = value.get("state")
    if state not in {"completed", "missing", "planned", "executing", "indeterminate"}:
        raise ReviewError("operation state invalid")
    return value


def observe_review(config, operation):
    value = observe_operation(operation["review_operation"])
    if value["state"] != "completed":
        return value["state"]
    result = value.get("result")
    # The App review result names no PR number, only its GitHub URL: bind that
    # path to this exact PR (as exact_issue does) so another PR sharing the same
    # head cannot lend its verdict. GitHub's review anchor is the one fragment.
    url = urlparse(result.get("url", "")) if isinstance(result, dict) and isinstance(result.get("url"), str) else None
    if value.get("kind") != "submit_pull_request_review" or url is None or url.scheme != "https" or url.netloc != "github.com" \
            or url.path.lower() != ("/" + config["repository"] + "/pull/" + str(operation["pr"])).lower() or url.query or not re.fullmatch(r"(pullrequestreview-[0-9]+)?", url.fragment) \
            or result.get("head_sha") != operation["head"] or result.get("verdict") not in {"allow", "block"}:
        raise ReviewError("review receipt does not match the exact head and pull request")
    if result["verdict"] == "allow" and operation.get("prior_review_operation") and not correction_review_is_explicit(config, operation, result):
        raise ReviewError("correction ALLOW does not explicitly correct the prior App BLOCK")
    return result["verdict"]


def correction_review_is_explicit(config, operation, result):
    review_id = result.get("review_id")
    if type(review_id) is not int or review_id < 1:
        return False
    try:
        raw = intake.command(["gh", "api", "repos/" + config["repository"] + "/pulls/" + str(operation["pr"]) + "/reviews", "--paginate"], timeout=int(config.get("command_timeout", 30)))
        reviews = json.loads(raw)
    except (intake.IntakeError, json.JSONDecodeError, TypeError, ValueError):
        return False
    if not isinstance(reviews, list):
        return False
    for review in reviews:
        if not isinstance(review, dict) or review.get("id") != review_id or review.get("commit_id") != operation["head"]:
            continue
        body = review.get("body")
        if not isinstance(body, str):
            return False
        lines = [line.strip() for line in body.splitlines()]
        verdict = "Dark-Factory-Review: allow " + operation["head"]
        correction = "Dark-Factory-Review-Correction: " + operation["prior_review_operation"]
        marker = "<!-- dark-factory-operation:" + operation["review_operation"] + ":"
        for index in range(len(lines) - 2):
            if lines[index] == verdict and lines[index + 1] == correction and lines[index + 2].startswith(marker):
                return True
        return False
    return False


def enqueue_request_digest(config, operation):
    # Matches the App's own request_digest: the canonical (unsorted, struct-
    # order) JSON of the exact EnqueuePullRequest fields, sha256-hexed. The
    # App's analogous observe_pull_request_merge guard compares this same way
    # (control-plane/src/github_app.rs:2752-2769) before trusting a completed
    # enqueue observation. EnqueuePullRequest::validate canonicalizes
    # operation_id to lowercase before that digest is computed
    # (control-plane/src/github_app.rs:3069-3073, canonical_operation_id at
    # :3351-3365), so an uppercase persisted id must be lowercased here too.
    expected = {"repository": config["repository"].lower(), "operation_id": operation["enqueue_operation"].lower(),
                "pull_number": operation["pr"], "head_sha": operation["head"], "base": operation["enqueue_base"]}
    if operation.get("reviewed_body_digest"):
        expected["reviewed_body_digest"] = operation["reviewed_body_digest"]
    return hashlib.sha256(json.dumps(expected, separators=(",", ":")).encode()).hexdigest()


def observe_enqueue(config, operation):
    value = observe_operation(operation["enqueue_operation"])
    if value["state"] != "completed":
        return value["state"]
    result = value.get("result")
    if value.get("kind") != "enqueue_pull_request" or value.get("request_digest") != enqueue_request_digest(config, operation) \
            or not isinstance(result, dict) or result.get("head_sha") != operation["head"] or result.get("pull_number") != operation["pr"]:
        raise ReviewError("enqueue receipt does not match the exact head")
    return "queued"


def observe_merge(config, operation):
    reviewed_body_digest = operation.get("reviewed_body_digest")
    if reviewed_body_digest is not None and (not isinstance(reviewed_body_digest, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", reviewed_body_digest)):
        raise ReviewError("merge observation lacks the reviewed body digest")
    arguments = {"repository": config["repository"], "enqueue_operation_id": operation["enqueue_operation"],
                 "pull_number": operation["pr"], "head_sha": operation["head"], "base": operation["enqueue_base"]}
    if reviewed_body_digest is not None:
        arguments["reviewed_body_digest"] = reviewed_body_digest
    result = bridge_call("observe_pull_request_merge", arguments)
    value = result.get("structuredContent")
    if result.get("isError") or not isinstance(value, dict) or value.get("pull_number") != operation["pr"] \
            or value.get("head_sha") != operation["head"] or value.get("base") != operation["enqueue_base"] \
            or value.get("state") not in {"ACTIVE_QUEUE", "MERGED_AFTER_ENQUEUE_ATTEMPT", "NOT_QUEUED"} \
            or value.get("pull_state") not in {"open", "closed"}:
        raise ReviewError("merge observation does not match the exact enqueue")
    return {"state": value["state"], "pull_state": value["pull_state"]}


def merge_failure_followup(config, operation):
    task_id = intake.sha_id("queue-failure", config["project_id"], config["repository"], str(operation["pr"]), operation["head"], operation["enqueue_operation"])
    return {"task_id": task_id, "incarnation_id": intake.sha_id("incarnation", task_id),
            "priority": operation["priority"],
            "title": "Reconcile dropped merge-queue entry for GitHub PR #" + str(operation["pr"]),
            "body": ("The completed App enqueue operation " + operation["enqueue_operation"] + " for " + config["repository"] + " PR #" + str(operation["pr"]) +
                     " at exact head " + operation["head"] + " was observed at " + str(operation["merge_observed_at"]) +
                     " as NOT_QUEUED while the pull request remained open. The queue run may have failed or dropped the entry. Route this causal notification to the original source owner/task " +
                     operation["source_marker"] + ". Inspect the exact queue/CI failure in that existing task and apply any ordinary source correction there. Do not create a replacement task, enqueue again, or replay the review. If App observation or authority is unresolved, escalate that capability failure with this receipt." )}


def enqueue_allowed(config, operation, journal_path, receipts):
    # One durable id per exact head, journaled before the write; an id an
    # operator already recorded is kept so a repair is never replayed.
    operation.setdefault("enqueue_operation", str(uuid.uuid5(uuid.NAMESPACE_URL, "dark-factory:host-enqueue:" + config["repository"] + ":" + str(operation["pr"]) + ":" + operation["head"])))
    operation.setdefault("enqueue_base", config.get("base", "main"))
    intake.atomic_json(journal_path, receipts)
    state = observe_enqueue(config, operation)
    if state == "missing" and not operation.get("enqueue_attempted"):
        operation["enqueue_attempted"] = True
        intake.atomic_json(journal_path, receipts)
        if not operation.get("reviewed_body_digest"):
            raise ReviewError("enqueue requires an operation-bound reviewed body digest")
        result = bridge_call("enqueue_pull_request", {"repository": config["repository"], "operation_id": operation["enqueue_operation"],
                                                      "pull_number": operation["pr"], "head_sha": operation["head"], "base": operation["enqueue_base"],
                                                      "reviewed_body_digest": operation["reviewed_body_digest"]})
        value = result.get("structuredContent")
        if result.get("isError"):
            # The App names refusals and conflicts; only those are concrete. An
            # indeterminate or unavailable write is observed on the next pass.
            text = str((result.get("content") or [{}])[0].get("text", ""))
            operation["enqueue_refusal"] = text[:1000]
            state = "refused" if text.split(":", 1)[0] in {"refused", "conflict", "invalid_input"} else "unresolved"
        elif not isinstance(value, dict) or value.get("head_sha") != operation["head"] or value.get("pull_number") != operation["pr"]:
            raise ReviewError("enqueue result does not match the exact head")
        else:
            state = "queued"
    elif state in {"missing", "planned"} and operation.get("enqueue_state") == "refused":
        # The App itself persists a released refusal claim as journal state
        # "planned", not "missing"; either must keep the concrete refusal
        # already recorded rather than degrade to generic "unresolved".
        state = "refused"
    elif state != "queued":
        state = "unresolved"
    operation["enqueue_state"] = state
    intake.atomic_json(journal_path, receipts)


def launch_review(config, path, pr, operation):
    # The existing process-group wrapper owns and verifies reviewer cleanup.
    # A parent subprocess timeout must not kill only the shell and orphan Codex.
    directory = Path(config["journal"]).parent / ("review-" + str(pr["number"]) + "-" + operation["head"])
    directory.mkdir(mode=0o700, exist_ok=True)
    body = directory / "body.md"
    body.write_text(pr["body"])
    env = dict(os.environ, DARK_FACTORY_REVIEW_PROVIDER=operation.get("provider", review_provider(config)),
               DARK_FACTORY_REVIEW_OPERATION_ID=operation["review_operation"],
               DARK_FACTORY_REVIEW_REMOTE="file://" + str(path.parent.parent))
    if operation.get("prior_review_operation"):
        env["DARK_FACTORY_REVIEW_CORRECTS_OPERATION_ID"] = operation["prior_review_operation"]
    else:
        env.pop("DARK_FACTORY_REVIEW_CORRECTS_OPERATION_ID", None)
    env.pop("DARK_FACTORY_REVIEW_EVIDENCE_FILE", None)
    with (directory / "launch.log").open("w") as output:
        return subprocess.run(["/bin/sh", "-c", '. "$1"; shift; go_gate_run_bounded "$@"', "review-process-owner",
                               str(HERE / "go-gate-environment.sh"), "1200", str(HERE / "cold-review.sh"),
                               config["repository"], str(pr["number"]), operation["head"], operation["base"], str(body)],
                              cwd=directory, env=env, stdout=output, stderr=subprocess.STDOUT).returncode


def review_body_path(config, pr, operation):
    return Path(config["journal"]).parent / ("review-" + str(pr["number"]) + "-" + operation["head"]) / "body.md"


def verify_review_body(config, pr, operation):
    try:
        raw = intake.command(["gh", "pr", "view", str(pr["number"]), "--repo", config["repository"], "--json", "body,headRefOid"], timeout=int(config.get("command_timeout", 30)))
        current = json.loads(raw)
        body = current["body"]
        head = current["headRefOid"]
        reviewed = review_body_path(config, pr, operation).read_text()
    except (intake.IntakeError, OSError, json.JSONDecodeError, KeyError, TypeError, ValueError) as exc:
        raise ReviewError("live pull request body is unavailable") from exc
    if head != operation["head"]:
        raise ReviewError("pull request head changed after review")
    if not isinstance(body, str) or body != reviewed:
        raise ReviewError("pull request body changed after review")
    operation["reviewed_body_digest"] = "sha256:" + hashlib.sha256(body.encode()).hexdigest()


def review_followup(config, operation, state):
    task = {"priority": operation["priority"], "title": "Resume publication review for GitHub PR #" + str(operation["pr"])}
    enqueue = operation.get("enqueue_state", "")
    task["task_id"] = intake.sha_id("review-result", config["project_id"], config["repository"], str(operation["pr"]), operation["head"], state + (":" + enqueue if enqueue else ""))
    task["incarnation_id"] = intake.sha_id("incarnation", task["task_id"])
    if state == "allow":
        action = ("Host intake enqueued this exact head onto " + operation["enqueue_base"] + " with App operation " + operation["enqueue_operation"] + " (state " + enqueue + "). " +
                  ("Observe the merge with observe_pull_request_merge using that enqueue operation; the merge queue and required CI stay authoritative. Never enqueue again yourself. " if enqueue == "queued" else
                   "The App refused it: " + operation.get("enqueue_refusal", "") + " Do not retry blindly; report the concrete refusal or raise a human request. " if enqueue == "refused" else
                   "Its outcome is unresolved. Observe that same operation; never replay the write or derive a replacement id. "))
    elif state == "block":
        action = "Read that exact operation and its GitHub review; route blocking findings to the original task. "
    elif state == "stale-body":
        action = ("No review was launched: the pull request body does not name this exact head, so it describes a predecessor. Replace the body with update_pull_request_body, "
                  "stating this head, the cumulative production-line delta to it and only checks run on it; keep the standalone source-issue footer. Host intake reviews it on its next pass. ")
    else:
        action = "The launch or submission is unresolved. Observe this operation; do not start another reviewer or invent a verdict. Report the concrete infrastructure blocker. "
    task["body"] = ("Resume publication for " + operation["source_marker"] + ". Host independent review for PR #" + str(operation["pr"]) +
                    " at exact head " + operation["head"] + " and base " + operation["base"] +
                    " has App operation " + operation["review_operation"] + " with state " + state + ". Host review exit: " + str(operation.get("review_exit", "not launched")) + ". " + action +
                    "Never submit your own verdict or run a nested cold-review. Merge and deployment remain separate delivery gates.")
    return task


def config_fingerprint(config):
    value = {key: config.get(key) for key in ("repository", "project_id", "overseer_agent_id", "review_mirror_root", "review_provider")}
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def legacy_config_fingerprint(config):
    # Pre-review_provider version-2 receipts were fingerprinted without that
    # field. Recognize their existing digest so upgrading this script does
    # not invalidate every journal already on disk; a receipt only earns the
    # new, provider-bound digest the next time it is written.
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
    provider = review_provider(config)
    if journal_path.exists():
        try:
            receipts = json.loads(journal_path.read_text())
        except (OSError, json.JSONDecodeError) as exc:
            raise ReviewError("review receipt is unreadable") from exc
        if receipts.get("version") != 2 or not isinstance(receipts.get("pulls"), dict) \
                or receipts.get("config_fingerprint") not in (config_fingerprint(config), legacy_config_fingerprint(config)):
            raise ReviewError("review receipt is invalid")
    else:
        receipts = {"version": 2, "config_fingerprint": config_fingerprint(config), "pulls": {}}
    messages = []
    page = receipts.get("discovery_page", 1)
    if type(page) is not int or page < 1:
        raise ReviewError("review receipt discovery page is invalid")
    discovered = list_prs(config, page)
    batch = discovery_batch_size(config)
    next_page = next_discovery_page(config, page, len(discovered))
    launched = False
    for pr in discovered:
        key = str(pr["number"]) + ":" + pr["headRefOid"]
        existing = receipts["pulls"].get(key)
        try:
            issue = linked_issue(config, pr, journal, existing)
        except Unproven as exc:
            messages.append("skipped PR #" + str(pr["number"]) + ": " + str(exc))
            continue
        if issue is None:
            continue
        if existing is None:
            operation = ready(config, path, pr, issue)
            operation["provider"] = provider
            receipts["pulls"][key] = operation
            intake.atomic_json(journal_path, receipts)
        else:
            operation = existing
            if operation.get("provider", "codex") != provider:
                raise ReviewError("review provider changed for an existing exact-head receipt")
            verify_existing(path, pr, operation)
        operation.setdefault("review_operation", str(uuid.uuid5(uuid.NAMESPACE_URL, "dark-factory:host-review:" + config["repository"] + ":" + str(pr["number"]) + ":" + operation["head"])))
        state = observe_review(config, operation)
        if state == "block" and not operation.get("prior_review_operation"):
            snapshot_path = review_body_path(config, pr, operation)
            try:
                snapshot = snapshot_path.read_text()
            except FileNotFoundError:
                snapshot = pr["body"]
            except OSError as exc:
                raise ReviewError("stale review body snapshot is unavailable") from exc
            if snapshot != pr["body"]:
                if not app_update_receipt(pr["body"], pr["number"], config["repository"]):
                    raise ReviewError("changed review body has no completed App metadata update receipt")
                prior = operation["review_operation"]
                operation["prior_review_operation"] = prior
                operation["prior_review_state"] = "block"
                operation["review_operation"] = str(uuid.uuid5(uuid.NAMESPACE_URL,
                    "dark-factory:host-review-correction:" + config["repository"] + ":" + str(pr["number"]) + ":" + operation["head"] + ":" + prior))
                operation["review_attempted"] = False
                operation.pop("review_exit", None)
                operation.pop("review_state", None)
                operation["correction_body"] = pr["body"]
                snapshot_path.write_text(pr["body"])
                intake.atomic_json(journal_path, receipts)
                state = observe_review(config, operation)
        if state == "missing" and not operation.get("review_attempted"):
            if operation["head"] not in pr["body"]:
                # The body still describes a predecessor head. A review would
                # only block on it, so wake the overseer and wait for the body.
                followup = review_followup(config, operation, "stale-body")
                if intake.task_state(config, followup) is None:
                    intake.enqueue(config, followup)
                    messages.append("woke PR #" + str(pr["number"]) + " stale body")
                continue
            if launched:
                continue
            # Persist before launching: a crash cannot authorize a second model
            # run while the first may still be submitting its exact-head verdict.
            operation["review_attempted"] = True
            intake.atomic_json(journal_path, receipts)
            launched = True
            operation["review_exit"] = launch_review(config, path, pr, operation)
            intake.atomic_json(journal_path, receipts)
            state = observe_review(config, operation)
        if state not in {"allow", "block"}:
            state = "unresolved"
        operation["review_state"] = state
        intake.atomic_json(journal_path, receipts)
        if state == "allow":
            # The body is an authorization input only before enqueue. Once a
            # completed enqueue is journaled, later metadata edits must not
            # block read-only merge reconciliation; the App receipt remains
            # bound to the persisted reviewed_body_digest.
            if not operation.get("reviewed_body_digest"):
                existing_enqueue = observe_enqueue(config, operation) if operation.get("enqueue_operation") else "missing"
                if existing_enqueue == "missing":
                    verify_review_body(config, pr, operation)
                elif existing_enqueue == "queued":
                    operation["enqueue_state"] = "queued"
            enqueue_allowed(config, operation, journal_path, receipts)
            if operation.get("enqueue_state") == "queued":
                merge = observe_merge(config, operation)
                operation["merge_state"] = merge["state"]
                operation["merge_pull_state"] = merge["pull_state"]
                operation["merge_observed_at"] = int(time.time())
                intake.atomic_json(journal_path, receipts)
                if merge["state"] == "NOT_QUEUED" and merge["pull_state"] == "open":
                    followup = merge_failure_followup(config, operation)
                    if intake.task_state(config, followup) is None:
                        intake.enqueue(config, followup)
                        messages.append("woke PR #" + str(pr["number"]) + " queue failure")
        followup = review_followup(config, operation, state)
        if intake.task_state(config, followup) is None:
            intake.enqueue(config, followup)
            messages.append("woke PR #" + str(pr["number"]) + " review " + state)
    receipts["discovery_page"] = next_page
    intake.atomic_json(journal_path, receipts)
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
