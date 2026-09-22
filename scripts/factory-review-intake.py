#!/usr/bin/env python3
"""Run one independent host review and wake its overseer with the App receipt."""
import argparse
import contextlib
from datetime import datetime
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import stat
import subprocess
import tempfile
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
FORMAL_SPEC = importlib.util.spec_from_file_location("factory_production_reviews", HERE / "factory-production-reviews.py")
formal = importlib.util.module_from_spec(FORMAL_SPEC)
FORMAL_SPEC.loader.exec_module(formal)
SHA = re.compile(r"^[0-9a-f]{40}$")
# ponytail: the existing controller owns one sequential review pass per home.
CUSTOMER_REVIEW = None


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


def linked_issue(config, pr, journal, existing=None, managed=None):
    body = pr["body"]
    if not isinstance(body, str):
        raise ReviewError("pull request body is invalid")
    footer = publication.terminal_footer(body)
    numbers = {int(value) for repository, value in publication.FOOTER.findall(body)
               if not repository or repository.casefold() == source_config(config)["repository"].casefold()}
    known = {record.get("number") for record in journal["issues"].values() if isinstance(record, dict) and record.get("managed")}
    matched = numbers & known
    if len(matched) > 1:
        raise ReviewError("pull request links multiple tracked source issues")
    if footer is None:
        return None
    if footer.group(2) and footer.group(2).casefold() != source_config(config)["repository"].casefold():
        return None  # Another source controller owns this fully qualified backlog.
    issue = int(footer.group(3))
    if issue in known:
        if managed is not None:
            controller, receipt, factoryctl = managed
            request = dict(receipt['request'], action='legacy_lineage', issue_number=issue)
            reply = controller.managed_api(factoryctl, Path(config['factory_home']), ['legacy_lineage'], request)
            if reply.get('state') not in {'imported', 'legacy_existing_work'} or not isinstance(reply.get('task_id'), str) or not intake.ID_RE.fullmatch(reply['task_id']):
                raise Unproven('retained source lineage is unavailable for footer #' + str(issue))
        return issue
    if matched:
        raise Unproven("terminal footer #" + str(issue) + " is not the tracked source #" + str(min(matched)) + " the body also links")
    if managed is None and existing is not None and existing.get("source_marker") == intake.source_marker(source_config(config), {"number": issue}):
        return issue
    # New managed work needs both imported acceptance lineage and a completed
    # PR publication receipt. The legacy App-created tracking-issue fallback
    # below still proves both objects; App authorship alone grants no approval.
    if not app_receipt(body, {"create_pull_request", "update_pull_request_body"}, pr["number"], config["repository"], "pull"):
        raise Unproven("footer #" + str(issue) + " is not an intake-managed source and PR #" + str(pr["number"]) + " has no completed App publication receipt")
    if managed is not None:
        controller, receipt, factoryctl = managed
        request = dict(receipt['request'], action='legacy_lineage', issue_number=issue)
        reply = controller.managed_api(factoryctl, Path(config['factory_home']), ['legacy_lineage'], request)
        if reply.get('state') == 'imported' and all(isinstance(reply.get(key), str) and intake.ID_RE.fullmatch(reply[key]) for key in ('acceptance_id', 'task_id')):
            return issue
        if reply.get('state') != 'not_found':
            raise Unproven('managed source lineage is unavailable for footer #' + str(issue))
        raise Unproven('source has no imported acceptance; review and accept it before publication')
    try:
        source = intake.exact_issue(config, issue)
    except intake.IssueBodyTooLarge as exc:
        raise Unproven("footer #" + str(issue) + " points to a source whose body exceeds the intake limit") from exc
    if not app_receipt(source["body"], {"create_issue"}, issue, config["repository"], "issues"):
        raise Unproven("footer #" + str(issue) + " is neither an intake-managed source nor an App-created tracking issue (no completed create_issue receipt)")
    return issue


def discovery_batch_size(config):
    # GitHub's REST pull-request endpoint caps per_page at 100.
    return 2 if CUSTOMER_REVIEW is not None else min(int(config.get("max_issues", 25)), 100)


def next_discovery_page(config, page, discovered):
    return 1 if discovered < discovery_batch_size(config) else page + 1


def list_prs(config, page=1):
    if CUSTOMER_REVIEW is not None:
        value = bridge_call("list_pull_requests", {"page": page}).get("structuredContent")
        if not isinstance(value, dict) or not isinstance(value.get("pull_requests"), list) or len(value["pull_requests"]) > 2:
            raise ReviewError("customer pull request page is invalid")
        normalized = []
        for item in value['pull_requests']:
            if not isinstance(item, dict) or type(item.get('number')) is not int or item['number'] < 1 or not isinstance(item.get('body'), str) or not isinstance(item.get('head_sha'), str) or not SHA.fullmatch(item['head_sha']) or not isinstance(item.get('base_sha'), str) or not SHA.fullmatch(item['base_sha']) or not isinstance(item.get('base_ref'), str):
                raise ReviewError('customer pull request is invalid')
            normalized_item = {'number': item['number'], 'body': item['body'], 'headRefOid': item['head_sha'], 'baseRefName': item['base_ref'], 'baseRefOid': item['base_sha']}
            if 'mergeable' in item or 'merge_state_status' in item:
                if type(item.get('mergeable')) is not bool or not isinstance(item.get('merge_state_status'), str):
                    raise ReviewError('customer pull request mergeability is invalid')
                normalized_item.update(mergeable=item['mergeable'], mergeStateStatus=item['merge_state_status'].upper())
            # A listing without mergeability is read exactly, bound to this listed head, by refresh_mergeability.
            normalized.append(normalized_item)
        return normalized
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
        item = {"number": value["number"], "headRefOid": head["sha"], "body": value.get("body") or ""}
        if "mergeable" in value or "mergeable_state" in value:
            item.update(mergeable=value.get("mergeable"), mergeStateStatus=str(value.get("mergeable_state", "")).upper())
        normalized.append(item)
    return normalized


def merge_conflict(pr):
    """Return whether GitHub has proved this exact head conflicts with its base."""
    return pr.get("mergeable") is False or pr.get("mergeStateStatus") in {"DIRTY", "CONFLICTING", "UNMERGEABLE"}


# GitHub's resolved, non-conflicting merge states. UNKNOWN (or a missing state) means
# GitHub has not computed mergeability for this head yet, whatever `mergeable` says.
RESOLVED_MERGE_STATES = {"CLEAN", "BLOCKED", "BEHIND", "HAS_HOOKS", "UNSTABLE"}


def require_mergeable(pr):
    if merge_conflict(pr):
        return False
    if pr.get("mergeable") is not True or pr.get("mergeStateStatus") not in RESOLVED_MERGE_STATES:
        raise ReviewError("pull request mergeability is unresolved")
    return True


def refresh_mergeability(config, number, head):
    """Read the exact pull request's mergeability; it only counts for the head under review."""
    if CUSTOMER_REVIEW is not None:
        value = bridge_call("list_pull_requests", {"page": 1, "pull_number": number}).get("structuredContent")
        pulls = value.get("pull_requests") if isinstance(value, dict) else None
        if not isinstance(pulls, list) or len(pulls) != 1:
            raise ReviewError("exact pull request mergeability is unavailable")
        item = pulls[0]
        current = {"number": item.get("number"), "headRefOid": item.get("head_sha"), "mergeable": item.get("mergeable"), "mergeStateStatus": str(item.get("merge_state_status", "")).upper()}
    else:
        raw = intake.command(["gh", "api", "repos/" + config["repository"] + "/pulls/" + str(number)], timeout=int(config.get("command_timeout", 30)))
        try:
            value = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise ReviewError("exact pull request mergeability is unavailable") from exc
        if not isinstance(value, dict):
            raise ReviewError("exact pull request mergeability is unavailable")
        current = {"number": value.get("number"), "headRefOid": (value.get("head") or {}).get("sha") if isinstance(value.get("head"), dict) else None,
                   "mergeable": value.get("mergeable"), "mergeStateStatus": str(value.get("mergeable_state", "")).upper()}
    if current["number"] != number:
        raise ReviewError("exact pull request read returned PR #" + str(current["number"]) + " instead of #" + str(number))
    if current["headRefOid"] != head:
        raise ReviewError("pull request head moved from " + str(head) + " to " + str(current["headRefOid"]) + " since discovery; this head is no longer under review")
    return current


def send_back_merge_conflict(config, operation, journal_path, receipts, messages, pr):
    """Send a proven conflict back to its source task once; the exact head then waits for a rebase."""
    if not operation.get("merge_conflict_sent_back"):
        send_back_source_task(config, operation, "merge conflict with main; rebase this Change onto the current main branch, then rerun the focused checks and republish.")
        operation["merge_conflict_sent_back"] = True
        intake.atomic_json(journal_path, receipts)
        messages.append("sent back PR #" + str(pr["number"]) + " for rebase")


def ready(config, path, pr, issue):
    base = pr.get("baseRefName") if CUSTOMER_REVIEW is not None else config.get("base", "main")
    if CUSTOMER_REVIEW is not None:
        if not isinstance(base, str) or not base or len(base.encode()) > 4096:
            raise ReviewError('base must be an explicit branch name')
        intake.command(['git', 'check-ref-format', 'refs/heads/' + base])
    elif not isinstance(base, str) or not re.fullmatch(r"[A-Za-z0-9_.-]{1,240}", base):
        raise ReviewError("base must be an explicit branch name")
    intake.command(["git", "-C", str(path), "fetch", "--no-tags", "origin", "+refs/heads/" + base + ":refs/remotes/origin/" + base, "+refs/pull/" + str(pr["number"]) + "/head:refs/pull/" + str(pr["number"]) + "/head"], timeout=120)
    head = intake.command(["git", "-C", str(path), "rev-parse", "refs/pull/" + str(pr["number"]) + "/head"]).strip()
    observed_base = intake.command(["git", "-C", str(path), "rev-parse", "refs/remotes/origin/" + base]).strip()
    if head != pr["headRefOid"] or not SHA.fullmatch(observed_base) or (CUSTOMER_REVIEW is not None and observed_base != pr["baseRefOid"]):
        raise ReviewError("mirror did not prove the App-reported exact head and base")
    marker = intake.source_marker(source_config(config), {"number": issue})
    return {"pr": pr["number"], "head": head, "base": observed_base, "source_marker": marker,
            "priority": int(config.get("priority_default", 0)), "enqueue_base": base}



def verify_existing(path, pr, operation):
    head, base = operation.get("head"), operation.get("base")
    if not isinstance(head, str) or not isinstance(base, str) or not SHA.fullmatch(head) or not SHA.fullmatch(base):
        raise ReviewError("review receipt is invalid")
    intake.command(["git", "-C", str(path), "fetch", "--no-tags", "origin", "+refs/pull/" + str(pr["number"]) + "/head:refs/pull/" + str(pr["number"]) + "/head"], timeout=120)
    observed = intake.command(["git", "-C", str(path), "rev-parse", "refs/pull/" + str(pr["number"]) + "/head"]).strip()
    if observed != pr["headRefOid"] or observed != head:
        raise ReviewError("mirror did not prove the App-reported exact head")
    intake.command(["git", "-C", str(path), "cat-file", "-e", base + "^{commit}"])


def customer_review(name, arguments):
    config, managed = CUSTOMER_REVIEW
    controller, receipt, factoryctl = managed
    arguments = dict(arguments)
    arguments.pop('repository', None)
    request = dict(receipt['request'], action='review', review=dict(arguments, tool=name))
    if name in {'submit_pull_request_review', 'enqueue_pull_request'}:
        request['issue_number'] = config.get('_review_issue', 0)
    reply = controller.managed_api(factoryctl, Path(config['factory_home']), ['review'], request)
    if reply.get('state') != 'ok' or not isinstance(reply.get('review'), dict):
        raise ReviewError('Customer review access unavailable (' + str(reply.get('state', 'unavailable')) + '); connect or refresh GitHub and bind the publication repository. Legacy credentials are never used.')
    return reply['review']


def source_config(config):
    return dict(config, repository=config.get('source_repository', config['repository']))


def bridge_call(name, arguments):
    if CUSTOMER_REVIEW is not None:
        try:
            reply = json.loads(customer_review(name, arguments).get("response", ""))
        except (TypeError, ValueError) as exc:
            raise ReviewError(name + " reply invalid") from exc
        if not isinstance(reply, dict) or not isinstance(reply.get("result"), dict):
            raise ReviewError(name + " unavailable")
        return reply["result"]
    bridge = os.environ.get("DARK_FACTORY_MAINTAINER_BRIDGE") or shutil.which("dark-factory-maintainer-mcp-bridge")
    if not bridge or not os.path.isabs(bridge):
        raise ReviewError("maintainer bridge is unavailable")
    metadata = Path(bridge).stat()
    if not stat.S_ISREG(metadata.st_mode) or not metadata.st_mode & stat.S_IXUSR or metadata.st_mode & 0o022:
        raise ReviewError("maintainer bridge is not a safe executable")
    request = {"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": name, "arguments": arguments}}
    try:
        response = subprocess.run([bridge], input=json.dumps(request) + "\n", capture_output=True, text=True, timeout=40, check=True, pass_fds=() if intake.CONTROLLER_LOCK_FD is None else (intake.CONTROLLER_LOCK_FD,))
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


def observe_review(config, operation, external_reviews=None):
    # External reviewers do not create a broker operation.  The bounded
    # controller poll is therefore the durable event boundary for an ALLOW or
    # BLOCK that names this exact published head.
    if external_reviews is not None and CUSTOMER_REVIEW is None:
        external = external_reviews.get((operation["pr"], operation["head"]))
        if isinstance(external, dict) and external.get("state") in {"allow", "block"}:
            operation["external_review"] = {
                "head": operation["head"],
                "state": external["state"],
                "observed_at": int(time.time()),
            }
            return external["state"]
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
        if CUSTOMER_REVIEW is not None:
            reviews = [bridge_call("observe_pull_request_review", {"pull_number": operation["pr"], "review_id": review_id}).get("structuredContent")]
        else:
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


def _merge_group_failure(config, operation):
    raw = intake.command(["gh", "api", "repos/" + config["repository"] + "/actions/runs?event=merge_group&per_page=100"], timeout=30)
    value = json.loads(raw)
    runs = value.get("workflow_runs") if isinstance(value, dict) else None
    if not isinstance(runs, list):
        raise ReviewError("merge_group run listing is invalid")
    lower_bound = operation.get("enqueue_observed_at")
    upper_bound = operation.get("merge_observed_at")
    if type(lower_bound) is not int or type(upper_bound) is not int:
        return [], []
    candidates = []
    for item in runs:
        if not isinstance(item, dict) or item.get("event") != "merge_group" or item.get("conclusion") in {"success", "cancelled"}:
            continue
        if not any(isinstance(pr, dict) and pr.get("number") == operation["pr"] for pr in item.get("pull_requests", [])):
            continue
        stamp = item.get("created_at") or item.get("run_started_at") or item.get("updated_at")
        try:
            stamp = int(datetime.fromisoformat(stamp.replace("Z", "+00:00")).timestamp())
        except (AttributeError, TypeError, ValueError):
            continue
        if type(lower_bound) is int and stamp < lower_bound or type(upper_bound) is int and stamp > upper_bound + 300:
            continue
        candidates.append((stamp, item))
    run = max(candidates, key=lambda value: value[0])[1] if candidates else None
    if not isinstance(run, dict) or not isinstance(run.get("id"), int):
        return [], []
    jobs_raw = intake.command(["gh", "api", "repos/" + config["repository"] + "/actions/runs/" + str(run["id"]) + "/jobs?per_page=32"], timeout=30)
    jobs_value = json.loads(jobs_raw)
    jobs = jobs_value.get("jobs") if isinstance(jobs_value, dict) else None
    if not isinstance(jobs, list):
        raise ReviewError("merge_group job listing is invalid")
    failed = [job for job in jobs if isinstance(job, dict) and job.get("conclusion") not in {"success", "skipped", None}]
    failures = []
    for job in failed[:8]:
        name = job.get("name") if isinstance(job.get("name"), str) else "unknown job"
        steps = [step.get("name") for step in job.get("steps", [])
                 if isinstance(step, dict) and step.get("conclusion") not in {"success", "skipped", None}
                 and isinstance(step.get("name"), str) and step.get("name")]
        failures.append((name[:160], ", ".join(step[:160] for step in steps[:4]) or "unknown step"))
    excerpt = ""
    try:
        logs = intake.command(["gh", "run", "view", str(run["id"]), "--repo", config["repository"], "--log-failed"], timeout=30)
        excerpt = " ".join(logs.split())[:1200]
    except intake.IntakeError:
        pass
    return failures, excerpt


def _source_task_id(config, operation):
    journal = intake.load_journal(Path(config["journal"]))
    marker = operation["source_marker"]
    for record in journal.get("issues", {}).values():
        if not isinstance(record, dict) or not record.get("managed") or not isinstance(record.get("number"), int):
            continue
        source = intake.source_marker(source_config(config), {"number": record["number"]})
        if source != marker:
            continue
        fingerprint = record.get("desired_fingerprint") or record.get("processed_fingerprint")
        if isinstance(record.get("operation"), dict) and intake.ID_RE.fullmatch(record["operation"].get("task_id", "")):
            return record["operation"]["task_id"]
        if isinstance(fingerprint, str) and fingerprint:
            return intake.sha_id("source", marker, fingerprint)
    # An operator-created Change has no intake issue, but the daemon binds
    # every pull request it publishes to the task that produced it. Without
    # this, every finding on a worker's PR was journaled as unroutable and
    # nobody acted on it (22 Sep 2026).
    number = operation.get("pr")
    if type(number) is not int:
        return ""
    try:
        with sqlite3.connect(Path(config["factory_home"], "factory.sqlite3").as_uri() + "?mode=ro", uri=True) as connection:
            # The publishing overseer task is bound too, without a Change;
            # the worker that owns the Change is the one to correct it.
            row = connection.execute("SELECT lower(hex(task_id)) FROM publication_tasks WHERE repository = ? AND pull_number = ? "
                                     "ORDER BY (change_id IS NOT NULL) DESC, created_at_ms DESC, lower(hex(task_id)) LIMIT 1",
                                     (config["repository"], number)).fetchone()
    except sqlite3.Error:
        return ""
    return row[0] if row and intake.ID_RE.fullmatch(row[0]) else ""


def merge_failure_note(config, operation):
    failures, excerpt = _merge_group_failure(config, operation)
    details = ["failures=" + (", ".join(name + " / " + step for name, step in failures) if failures else "unavailable")]
    details.append("log=" + (excerpt if excerpt else "unavailable"))
    return "merge queue CI failed: " + "; ".join(details) + ". Exact head " + operation["head"] + "."


def gate_failure_note(receipt, operation):
    tests = []
    log = receipt.with_suffix(".log")
    try:
        # Only failure lines name a test: Go's "--- FAIL: Name" (subtests are
        # indented), unittest's "FAIL: name" / "ERROR: name". Matching every
        # "test" word reported "test-renderer" and "testing" from ordinary log text.
        tests = list(dict.fromkeys(re.findall(r"(?m)^\s*(?:--- FAIL|FAIL|ERROR): (\S+)", log.read_text(encoding="utf-8"))))[:16]
    except OSError:
        pass
    return "pre-review full gate failed: tests=" + (", ".join(tests) if tests else "unavailable") + ". Exact head " + operation["head"] + "."


def send_back_merge_failure(config, operation):
    return send_back_source_task(config, operation, merge_failure_note(config, operation))


def gate_ran(operation):
    """True when the journaled gate failure is a real gate exit, not a wrapper refusal."""
    try:
        code = json.loads(Path(operation["gate_evidence"]).read_text(encoding="utf-8")).get("exit_code")
    except (KeyError, TypeError, OSError, ValueError):
        return False
    return isinstance(code, int) and code != 0 and code not in {64, 125, 126, 127}


def send_back_source_task(config, operation, note):
    task_id = _source_task_id(config, operation)
    if not task_id:
        # An operator-created Change has no intake issue to route through.
        # Keep the finding on the receipt; raising here stopped every
        # other pull request in the tick.
        operation["send_back_unroutable"] = note
        return note
    home = Path(config["factory_home"])
    env = os.environ.copy()
    env["DARK_FACTORY_SOCKET"] = str(home / "runtimes" / "factory.sock")
    env["DARK_FACTORY_OPERATOR_TOKEN_FILE"] = str(home / "operator.token")
    intake.command(["factoryctl", "task", "send-back", "--task", task_id, "--note", note], env=env, timeout=int(config.get("command_timeout", 30)))
    return note


def enqueue_allowed(config, operation, journal_path, receipts):
    require_legacy_home(config)
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
    if state == "queued":
        operation.setdefault("enqueue_observed_at", int(time.time()))
    intake.atomic_json(journal_path, receipts)


def launch_review(config, path, pr, operation):
    require_legacy_home(config)
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
    env.pop("DARK_FACTORY_REVIEW_ADAPTER_CONTEXT", None)
    if CUSTOMER_REVIEW is not None:
        _, receipt, factoryctl = CUSTOMER_REVIEW[1]
        request = dict(receipt["request"], action="review", issue_number=config["_review_issue"],
                       review={"tool": "submit_pull_request_review", "pull_number": operation["pr"], "head_sha": operation["head"], "operation_id": operation["review_operation"]})
        if operation.get("prior_review_operation"):
            request["review"]["corrects_review_operation_id"] = operation["prior_review_operation"]
        context = directory / "adapter.json"
        intake.atomic_json(context, {"home": config["factory_home"], "repository": config["repository"], "request": request})
        os.chmod(context, 0o600)
        env["DARK_FACTORY_MAINTAINER_BRIDGE"] = str(factoryctl)
        env["DARK_FACTORY_REVIEW_ADAPTER_CONTEXT"] = str(context)
        for key in ("DARK_FACTORY_OPERATOR_TOKEN_FILE", "DARK_FACTORY_ATTEMPT_TOKEN_FILE", "DARK_FACTORY_SOCKET", "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"):
            env.pop(key, None)
    if operation.get("gate_evidence"):
        env["DARK_FACTORY_REVIEW_EVIDENCE_FILE"] = operation["gate_evidence"]
    with (directory / "launch.log").open("w") as output:
        process = subprocess.Popen(["/bin/sh", "-c", '. "$1"; shift; go_gate_run_bounded "$@"', "review-process-owner",
                                    str(HERE / "go-gate-environment.sh"), "1200", str(HERE / "cold-review.sh"),
                                    config["repository"], str(pr["number"]), operation["head"], operation["base"], str(body)],
                                   cwd=directory, env=env, stdout=output, stderr=subprocess.STDOUT,
                                   pass_fds=() if intake.CONTROLLER_LOCK_FD is None else (intake.CONTROLLER_LOCK_FD,))
        started = process_start(process.pid)
        activity = {"operation": operation["review_operation"], "pr": pr["number"], "head": operation["head"],
                    "repository": config["repository"], "pid": process.pid, "process_start": started,
                    "started_at": int(time.time() * 1000)}
        if started:
            intake.atomic_json(review_activity_path(config, pr, operation), activity)
        status = process.wait()
        if started:
            activity.update({"finished_at": int(time.time() * 1000), "exit": status})
            intake.atomic_json(review_activity_path(config, pr, operation), activity)
        return status


def process_start(pid):
    try:
        result = subprocess.run(["/bin/ps", "-o", "lstart=", "-p", str(pid)], capture_output=True, text=True, timeout=5)
        value = result.stdout.strip()
        return value if result.returncode == 0 and value else ""
    except (OSError, subprocess.SubprocessError):
        return ""


def review_body_path(config, pr, operation):
    return Path(config["journal"]).parent / ("review-" + str(pr["number"]) + "-" + operation["head"]) / "body.md"


def review_activity_path(config, pr, operation):
    return Path(config["journal"]).parent / ("review-" + str(pr["number"]) + "-" + operation["head"]) / "activity.json"


def run_full_gate(path, operation, destination):
    if not (path / "HEAD").is_file():
        raise ReviewError("review mirror is not a usable bare repository")
    destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    worktree = Path(tempfile.mkdtemp(prefix="gate-", dir=destination.parent))
    worktree.rmdir()
    try:
        intake.command(["git", "-C", str(path), "worktree", "add", "--detach", str(worktree), operation["head"]], timeout=120)
        log = destination.with_suffix(".gate.log")
        with log.open("w", encoding="utf-8") as output:
            process = subprocess.run(["/bin/sh", "-c", '. "$1"; shift; go_gate_run_bounded "$@"', "review-gate-owner",
                                      str(HERE / "go-gate-environment.sh"), "1800", str(worktree / "scripts/local-ci.sh")],
                                     cwd=worktree, stdout=output, stderr=subprocess.STDOUT, timeout=1860)
        receipt = destination.with_suffix(".gate.json")
        receipt.write_text(json.dumps({"head": operation["head"], "base": operation["base"], "exit_code": process.returncode}) + "\n", encoding="utf-8")
        return receipt
    finally:
        subprocess.run(["git", "-C", str(path), "worktree", "remove", "--force", str(worktree)], check=False,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def verify_review_body(config, pr, operation):
    try:
        if CUSTOMER_REVIEW is not None:
            pulls = bridge_call("list_pull_requests", {"page": 1, "pull_number": pr["number"]}).get("structuredContent", {}).get("pull_requests")
            if not isinstance(pulls, list) or len(pulls) != 1 or pulls[0].get("number") != pr["number"]:
                raise ReviewError("exact customer pull request is unavailable")
            body, head = pulls[0]["body"], pulls[0]["head_sha"]
        else:
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
    elif state.startswith("stale-body:"):
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
    config = source_config(config)
    value = {key: config.get(key) for key in ("repository", "project_id", "overseer_agent_id", "review_mirror_root", "review_provider")}
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def legacy_config_fingerprint(config):
    config = source_config(config)
    # Pre-review_provider version-2 receipts were fingerprinted without that
    # field. Recognize their existing digest so upgrading this script does
    # not invalidate every journal already on disk; a receipt only earns the
    # new, provider-bound digest the next time it is written.
    value = {key: config.get(key) for key in ("repository", "project_id", "overseer_agent_id", "review_mirror_root")}
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def require_legacy_home(config):
    if CUSTOMER_REVIEW is not None:
        customer_review("configuration", {})
        return
    home = Path(config['factory_home'])
    if not home.is_dir() or home.stat().st_uid != os.geteuid():
        raise ReviewError('legacy review factory home is unavailable')
    try:
        (Path(config['factory_home']) / 'maintainer.json').lstat()
    except FileNotFoundError:
        return
    raise ReviewError('Legacy review is owner-only and stops after customer GitHub opt-in, including disconnect. Use the installed customer publication workflow; do not restart the legacy bridge.')


@contextlib.contextmanager
def review_ownership(config, inherited):
    # ponytail: one CLI pass owns one factory. Parallel reviews require passing
    # the retained descriptor explicitly instead of this process-local handle.
    path = Path(str(Path(config['factory_home']).resolve()) + '.autonomy.lock')
    with contextlib.ExitStack() as ownership:
        if inherited is None:
            spec = importlib.util.spec_from_file_location('factory_autonomy', HERE / 'factory-autonomy.py')
            controller = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(controller)
            inherited = ownership.enter_context(controller.managed_lock(path))
        else:
            proof, current = os.fstat(inherited), path.lstat()
            if not stat.S_ISREG(proof.st_mode) or proof.st_uid != os.geteuid() or stat.S_IMODE(proof.st_mode) != 0o600 or proof.st_nlink != 1 or (proof.st_dev, proof.st_ino) != (current.st_dev, current.st_ino):
                raise ReviewError('legacy controller ownership is invalid')
            fcntl.flock(inherited, fcntl.LOCK_EX | fcntl.LOCK_NB)
        previous = intake.CONTROLLER_LOCK_FD
        intake.CONTROLLER_LOCK_FD = inherited
        try:
            yield
        finally:
            intake.CONTROLLER_LOCK_FD = previous


def run_once(config, managed=None, controller_lock_fd=None):
    global CUSTOMER_REVIEW
    config = dict(intake.validate_config(config))
    previous = CUSTOMER_REVIEW
    if managed is not None:
        CUSTOMER_REVIEW = config, managed
    try:
        return run_owned(config, managed, controller_lock_fd)
    finally:
        CUSTOMER_REVIEW = previous


def run_owned(config, managed, controller_lock_fd):
    with review_ownership(config, controller_lock_fd):
        require_legacy_home(config)
        if CUSTOMER_REVIEW is not None:
            route = customer_review("configuration", {})
            config["source_repository"] = config["repository"]
            config["repository"] = route["repository"]
        path = mirror(config)
        journal_path = Path(config["journal"] + ".reviews.json")
        lock_path = Path(str(journal_path) + ".lock")
        journal = intake.load_journal(Path(config["journal"]))
        intake.bind_journal(source_config(config), journal)
        lock_path.parent.mkdir(parents=True, exist_ok=True)
        with lock_path.open("a+") as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as exc:
                raise ReviewError("another review intake process owns the journal") from exc
            return run_locked(config, path, journal, journal_path, managed)


def enqueue_followup(config, followup, pr, journal, managed):
    require_legacy_home(config)
    if managed is not None:
        # Review may run for minutes. Recheck withdrawal/live authority at the
        # write boundary and bind this generated operator task to the frozen route.
        try:
            if linked_issue(config, pr, journal, managed=managed) is None:
                return False
        except Unproven:
            return False
        followup['repository_id'] = managed[1]['request']['configuration']['target_repository_id']
    intake.enqueue(config, followup)
    return True


def run_locked(config, path, journal, journal_path, managed=None):
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
    external_reviews = {}
    if CUSTOMER_REVIEW is None:
        try:
            external_reviews, _, _, _ = formal.collect(config["repository"])
        except (intake.IntakeError, formal.intake.IntakeError, ValueError, json.JSONDecodeError, OSError):
            # The App/customer route remains authoritative when configured;
            # an unavailable poll is not evidence for either verdict.
            external_reviews = {}
    messages = []
    failures = []
    page = receipts.get("discovery_page", 1)
    if type(page) is not int or page < 1:
        raise ReviewError("review receipt discovery page is invalid")
    discovered = list_prs(config, page)
    batch = discovery_batch_size(config)
    next_page = next_discovery_page(config, page, len(discovered))
    launched = gated = deferred = False
    for pr in discovered:
        key = str(pr["number"]) + ":" + pr["headRefOid"]
        if config.get("source_repository", config["repository"]).casefold() != config["repository"].casefold():
            key = config["repository"].casefold() + ":" + key
        existing = receipts["pulls"].get(key)
        try:
            issue = linked_issue(config, pr, journal, existing, managed)
            if issue is None:
                continue
            config["_review_issue"] = issue
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
            if merge_conflict(pr):
                send_back_merge_conflict(config, operation, journal_path, receipts, messages, pr)
                continue
            if "mergeable" not in pr or "mergeStateStatus" not in pr:
                # GitHub's list endpoint omits mergeability; only the exact PR read has it.
                pr.update(refresh_mergeability(config, pr["number"], operation["head"]))
                if merge_conflict(pr):
                    send_back_merge_conflict(config, operation, journal_path, receipts, messages, pr)
                    continue
            require_mergeable(pr)
            operation.setdefault("review_operation", str(uuid.uuid5(uuid.NAMESPACE_URL, "dark-factory:host-review:" + config["repository"] + ":" + str(pr["number"]) + ":" + operation["head"])))
            state = observe_review(config, operation, external_reviews)
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
                    state = observe_review(config, operation, external_reviews)
            if operation.get("gate_state") == "failed" and not gate_ran(operation):
                # Host error, not a test result (the pre-#1028 wrapper exit 64
                # receipts): forget it so the repaired host gates this head again.
                for stale in ("gate_state", "gate_failure_note", "gate_evidence", "gate_failure_sent_back"):
                    operation.pop(stale, None)
                intake.atomic_json(journal_path, receipts)
            if operation.get("gate_state") == "failed":
                if not operation.get("gate_failure_sent_back"):
                    note = operation.get("gate_failure_note", "pre-review full gate failed: tests=unavailable. Exact head " + operation["head"] + ".")
                    send_back_source_task(config, operation, note)
                    operation["gate_failure_sent_back"] = True
                    intake.atomic_json(journal_path, receipts)
                    messages.append("sent back PR #" + str(pr["number"]) + " pre-review gate failure: " + note)
                continue
            if state == "missing" and not operation.get("review_attempted"):
                if operation["head"] not in pr["body"]:
                    # The body still describes a predecessor head. A review would
                    # only block on it, so wake the overseer and wait for the body.
                    # One wake per distinct body: a rewrite that still omits the head wakes again.
                    followup = review_followup(config, operation, "stale-body:" + hashlib.sha256(pr["body"].encode()).hexdigest())
                    if intake.task_state(config, followup) is None and enqueue_followup(config, followup, pr, journal, managed):
                        messages.append("woke PR #" + str(pr["number"]) + " stale body")
                    continue
                if launched or gated:
                    deferred = deferred or gated
                    continue
                if (path / "HEAD").is_file():
                    # One full gate per tick, pass or fail: the tick holds the
                    # controller lock, and on 22 Sep 2026 a run of failing gates
                    # held it for hours while the release lane waited. A deferred
                    # PR keeps this discovery page for the next tick.
                    gated = True
                    try:
                        evidence = run_full_gate(path, operation, review_body_path(config, pr, operation))
                        receipt = json.loads(evidence.read_text(encoding="utf-8"))
                    except (OSError, ValueError, json.JSONDecodeError, intake.IntakeError, ReviewError, subprocess.SubprocessError) as exc:
                        operation["gate_state"] = "failed"
                        operation["gate_failure_note"] = "pre-review full gate unavailable: " + str(exc)[:300] + ". Exact head " + operation["head"] + "."
                        intake.atomic_json(journal_path, receipts)
                        send_back_source_task(config, operation, operation["gate_failure_note"])
                        operation["gate_failure_sent_back"] = True
                        intake.atomic_json(journal_path, receipts)
                        messages.append("sent back PR #" + str(pr["number"]) + " pre-review gate failure: " + operation["gate_failure_note"])
                        continue
                    # go_gate_run_bounded's own statuses (64 refused arguments,
                    # 125..127 supervisor or exec failure) mean nothing ran: name
                    # the host blocker instead of sending a working head back, and
                    # journal no gate verdict so the repaired host gates it again.
                    if receipt.get("exit_code") in {64, 125, 126, 127}:
                        raise ReviewError("pre-review gate could not run: bounded gate wrapper exit " + str(receipt["exit_code"]) +
                                          ", nothing ran. Exact head " + operation["head"] + ".")
                    operation["gate_evidence"] = str(evidence)
                    if receipt.get("exit_code") != 0:
                        operation["gate_state"] = "failed"
                        operation["gate_failure_note"] = gate_failure_note(evidence, operation)
                        intake.atomic_json(journal_path, receipts)
                        send_back_source_task(config, operation, operation["gate_failure_note"])
                        operation["gate_failure_sent_back"] = True
                        intake.atomic_json(journal_path, receipts)
                        messages.append("sent back PR #" + str(pr["number"]) + " pre-review gate failure: " + operation["gate_failure_note"])
                        continue
                    operation["gate_state"] = "passed"
                # Persist before launching: a crash cannot authorize a second model
                # run while the first may still be submitting its exact-head verdict.
                operation["review_attempted"] = True
                intake.atomic_json(journal_path, receipts)
                launched = True
                operation["review_exit"] = launch_review(config, path, pr, operation)
                intake.atomic_json(journal_path, receipts)
                state = observe_review(config, operation, external_reviews)
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
                current = refresh_mergeability(config, pr["number"], operation["head"])
                if merge_conflict(current):
                    send_back_merge_conflict(config, operation, journal_path, receipts, messages, pr)
                    continue
                require_mergeable(current)
                enqueue_allowed(config, operation, journal_path, receipts)
                if operation.get("enqueue_state") == "refused":
                    try:
                        current = refresh_mergeability(config, pr["number"], operation["head"])
                    except (ReviewError, intake.IntakeError):
                        current = None
                    if current is not None and merge_conflict(current):
                        send_back_merge_conflict(config, operation, journal_path, receipts, messages, pr)
                        continue
                if operation.get("enqueue_state") == "queued":
                    merge = observe_merge(config, operation)
                    operation["merge_state"] = merge["state"]
                    operation["merge_pull_state"] = merge["pull_state"]
                    operation["merge_observed_at"] = int(time.time())
                    intake.atomic_json(journal_path, receipts)
                    if merge["state"] == "NOT_QUEUED" and merge["pull_state"] == "open" and not operation.get("merge_failure_sent_back"):
                        note = send_back_merge_failure(config, operation)
                        operation["merge_failure_sent_back"] = True
                        operation["merge_failure_note"] = note
                        intake.atomic_json(journal_path, receipts)
                        messages.append("sent back PR #" + str(pr["number"]) + " queue failure: " + note)
            followup = review_followup(config, operation, state)
            if intake.task_state(config, followup) is None and enqueue_followup(config, followup, pr, journal, managed):
                messages.append("woke PR #" + str(pr["number"]) + " review " + state)
        except Unproven as exc:
            messages.append("skipped PR #" + str(pr["number"]) + ": " + str(exc))
            continue
        except (ReviewError, intake.IntakeError) as exc:
            # One pull request's host failure must not starve the others;
            # the tick still fails closed with every blocker named.
            failures.append("PR #" + str(pr["number"]) + ": " + str(exc))
            continue
    receipts["discovery_page"] = page if deferred else next_page
    intake.atomic_json(journal_path, receipts)
    if failures:
        raise ReviewError("; ".join(failures))
    return messages


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("config", type=Path)
    parser.add_argument("--once", action="store_true")
    parser.add_argument("--controller-lock-fd", type=int)
    parser.add_argument("--managed-migration", type=Path)
    parser.add_argument("--factoryctl", type=Path)
    args = parser.parse_args(argv)
    try:
        config = json.loads(args.config.read_text())
        managed = None
        if args.managed_migration is not None:
            spec = importlib.util.spec_from_file_location('factory_autonomy', HERE / 'factory-autonomy.py')
            controller = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(controller)
            home, state, identity = controller.managed_paths(Path(config['factory_home']))
            if args.managed_migration != state / 'migration.json' or args.factoryctl is None or not args.factoryctl.is_absolute():
                raise ReviewError('managed review requires the installed controller receipt and CLI')
            receipt = controller.managed_read(args.managed_migration, maximum=4 << 20)
            if not receipt or receipt.get('phase') != 'completed' or receipt.get('home_identity') != identity or json.loads(receipt['config']) != config:
                raise ReviewError('managed review migration does not match the frozen configuration')
            managed = controller, receipt, args.factoryctl
        print(json.dumps({"ok": True, "messages": run_once(config, managed, args.controller_lock_fd)}))
        return 0
    except (OSError, ValueError, json.JSONDecodeError, intake.IntakeError, ReviewError) as exc:
        print("factory-review-intake: " + str(exc), file=__import__("sys").stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
