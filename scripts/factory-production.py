#!/usr/bin/env python3
"""Collect host-owned GitHub, review, and release facts for the factory floor."""
import argparse
import importlib.util
import json
import re
import time
from pathlib import Path


HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("factory_intake", HERE / "factory-intake.py")
intake = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(intake)

REPOSITORY = re.compile(r"^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$")
SHA = re.compile(r"^[0-9a-f]{40}$")
MAX_PRS = 100
MAX_RUNS = 100
MAX_JOBS = 32
MAX_JOURNAL = 4 << 20


def text(value, limit=500):
    return value[:limit] if isinstance(value, str) else ""


def sha(value):
    return value if isinstance(value, str) and SHA.fullmatch(value) else ""


def number(value):
    return value if type(value) is int and value > 0 else None


def read_journal(path):
    """Read a configured local receipt without exposing its contents on failure."""
    try:
        if not isinstance(path, str) or not Path(path).is_absolute():
            raise ValueError
        file = Path(path)
        if file.stat().st_size > MAX_JOURNAL:
            raise ValueError
        value = json.loads(file.read_text(encoding="utf-8"))
        if not isinstance(value, dict):
            raise ValueError
        return value, ""
    except (OSError, ValueError, json.JSONDecodeError):
        return {}, "journal"


def configured_paths(config, key):
    paths = config.get("observation_journals", {})
    value = paths.get(key, []) if isinstance(paths, dict) else []
    if not isinstance(value, list) or len(value) > 32 or any(not isinstance(path, str) or not Path(path).is_absolute() for path in value):
        raise ValueError("observation journal paths must be absolute lists")
    return value


def github(repository, endpoint):
    raw = intake.command(["gh", "api", "repos/" + repository + endpoint,
                          "--method", "GET"], timeout=30)
    value = json.loads(raw)
    if not isinstance(value, (dict, list)):
        raise ValueError
    return value


def review_receipts(paths):
    reviewers, markers, merges, unavailable = [], {}, {}, False
    for path in paths:
        journal, error = read_journal(path)
        if error or journal.get("version") != 2 or not isinstance(journal.get("pulls"), dict):
            unavailable = True
            continue
        for operation in journal["pulls"].values():
            if not isinstance(operation, dict):
                continue
            pr, head = number(operation.get("pr")), sha(operation.get("head"))
            operation_id = text(operation.get("review_operation"), 128)
            if pr is None or not head or not operation_id:
                continue
            outcome = operation.get("review_state")
            # An attempted host launch without a retained exit is not evidence
            # that a reviewer is currently running or that it reached GitHub.
            known = outcome in {"allow", "block"} and type(operation.get("review_exit")) is int
            state = outcome if known else "unknown"
            receipt = {"id": operation_id, "number": pr, "head": head,
                       "name": "host review", "provider": text(operation.get("provider"), 32) or "codex",
                       "state": state}
            reviewers.append(receipt)
            if known:
                markers[(pr, head)] = {"head": head, "state": outcome}
            merge = text(operation.get("merge_state"), 32)
            if merge:
                merges[(pr, head)] = merge
    return reviewers, markers, merges, unavailable


def release_receipts(paths):
    deliveries, unavailable = {}, False
    for path in paths:
        journal, error = read_journal(path)
        if error or journal.get("version") != 1 or not isinstance(journal.get("releases"), dict):
            unavailable = True
            continue
        for receipt in journal["releases"].values():
            if not isinstance(receipt, dict):
                continue
            revision = sha(receipt.get("sha"))
            if not revision:
                continue
            verification = receipt.get("verification") if isinstance(receipt.get("verification"), dict) else {}
            verified = (receipt.get("state") == "verified" and verification.get("healthy") is True
                        and verification.get("sha") == revision)
            identity = text(verification.get("id"), 128) or text(receipt.get("deployment_id"), 128) or "release:" + revision
            destination = text(verification.get("destination"), 160) or text(receipt.get("destination"), 160) or "production"
            key = (identity, destination, revision)
            delivery = deliveries.setdefault(key, {"id": identity, "kind": "release", "destination": destination,
                                                    "revision": revision, "state": "verified" if verified else "unknown",
                                                    "pull_requests": []})
            if not verified and delivery["state"] != "verified":
                delivery["state"] = text(receipt.get("state"), 32) or "unknown"
            url = text(verification.get("url"), 1000) or text(receipt.get("url"), 1000)
            if url:
                delivery["url"] = url
            if verified and type(receipt.get("verified_at")) is int:
                delivery["verified_at"] = receipt["verified_at"]
            sources = receipt.get("delivery_sources")
            if not isinstance(sources, list):
                sources = [{"pr": receipt.get("pr")}]
            for source in sources:
                source_pr = number(source.get("pr")) if isinstance(source, dict) else None
                if source_pr is not None and source_pr not in delivery["pull_requests"]:
                    delivery["pull_requests"].append(source_pr)
    return list(deliveries.values()), unavailable


def pull_requests(value, markers, merges):
    if not isinstance(value, list):
        raise ValueError
    result, heads = [], {}
    for item in value[:MAX_PRS]:
        if not isinstance(item, dict):
            continue
        pr, head = number(item.get("number")), sha((item.get("head") or {}).get("sha") if isinstance(item.get("head"), dict) else "")
        if pr is None or not head:
            continue
        branch = text(item["head"].get("ref"), 240)
        review = markers.get((pr, head), {"head": head, "state": "unknown"})
        record = {"number": pr, "title": text(item.get("title")), "url": text(item.get("html_url"), 1000),
                  "head": head, "branch": branch, "base": text((item.get("base") or {}).get("ref") if isinstance(item.get("base"), dict) else "", 240),
                  "state": text(item.get("state"), 32) or "unknown", "review": review}
        merge = merges.get((pr, head), "")
        if merge:
            record["merge"] = merge
        result.append(record)
        heads.setdefault(head, []).append(pr)
    return result, heads, max(0, len(value) - MAX_PRS)


def run_pull_requests(run, heads):
    linked = set(heads.get(sha(run.get("head_sha")), []))
    values = run.get("pull_requests")
    if isinstance(values, list):
        for item in values:
            pr = number(item.get("number")) if isinstance(item, dict) else None
            if pr is not None:
                linked.add(pr)
    return sorted(linked)


def checks(repository, value, heads):
    if not isinstance(value, dict) or not isinstance(value.get("workflow_runs"), list):
        raise ValueError
    result, seen, overflow = [], set(), 0
    for run in value["workflow_runs"][:MAX_RUNS]:
        if not isinstance(run, dict) or number(run.get("id")) is None:
            continue
        run_id = str(run["id"])
        if run_id in seen:
            continue
        seen.add(run_id)
        linked = run_pull_requests(run, heads)
        if not linked:
            continue
        jobs, omitted = [], 0
        try:
            response = github(repository, "/actions/runs/" + run_id + "/jobs?per_page=" + str(MAX_JOBS))
            values = response.get("jobs", [])
            if not isinstance(values, list):
                raise ValueError
            omitted = max(0, int(response.get("total_count", len(values))) - MAX_JOBS)
            for job in values[:MAX_JOBS]:
                if not isinstance(job, dict) or number(job.get("id")) is None:
                    continue
                jobs.append({"id": str(job["id"]), "name": text(job.get("name")),
                             "state": text(job.get("status"), 32) or "unknown",
                             "conclusion": text(job.get("conclusion"), 32), "url": text(job.get("html_url"), 1000)})
        except (intake.IntakeError, ValueError, json.JSONDecodeError):
            overflow += 1
        check = {"id": run_id, "name": text(run.get("name")), "revision": sha(run.get("head_sha")),
                 "scope": text(run.get("event"), 64), "state": text(run.get("status"), 32) or "unknown",
                 "conclusion": text(run.get("conclusion"), 32), "url": text(run.get("html_url"), 1000),
                 "pull_requests": linked, "jobs": jobs}
        if omitted:
            check["overflow"] = omitted
        result.append(check)
    return result, overflow + max(0, len(value["workflow_runs"]) - MAX_RUNS)


def collect(config):
    """Return one read-only ProductionObservation-compatible mapping."""
    if not isinstance(config, dict) or not isinstance(config.get("repository"), str) or not REPOSITORY.fullmatch(config["repository"]):
        raise ValueError("repository must be OWNER/REPOSITORY")
    # Customer routes use the Maintainer App; this collector deliberately has
    # no credential or bridge fallback outside the host controller's own path.
    if config.get("host_controller") is not True:
        return {"repository": config["repository"], "observed_at": int(time.time()), "pull_requests": [], "checks": [], "reviewers": [], "deliveries": [], "unavailable": "host_controller_only"}
    review_paths, release_paths = configured_paths(config, "reviews"), configured_paths(config, "releases")
    reviewers, markers, merges, review_unavailable = review_receipts(review_paths)
    deliveries, release_unavailable = release_receipts(release_paths)
    observation = {"repository": config["repository"], "observed_at": int(time.time()), "pull_requests": [], "checks": [],
                   "reviewers": reviewers, "deliveries": deliveries}
    unavailable = []
    if review_unavailable:
        unavailable.append("review_journal")
    if release_unavailable:
        unavailable.append("release_journal")
    try:
        prs = github(config["repository"], "/pulls?state=open&per_page=" + str(MAX_PRS))
        observation["pull_requests"], heads, pr_overflow = pull_requests(prs, markers, merges)
        runs = github(config["repository"], "/actions/runs?per_page=" + str(MAX_RUNS))
        observation["checks"], run_overflow = checks(config["repository"], runs, heads)
        observation["overflow"] = pr_overflow + run_overflow
    except (intake.IntakeError, ValueError, json.JSONDecodeError):
        unavailable.append("github")
    if observation.get("overflow") == 0:
        observation.pop("overflow", None)
    if unavailable:
        observation["unavailable"] = ",".join(unavailable)
    return observation


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("config", type=Path)
    args = parser.parse_args(argv)
    print(json.dumps(collect(json.loads(args.config.read_text(encoding="utf-8"))), sort_keys=True))


if __name__ == "__main__":
    main()
