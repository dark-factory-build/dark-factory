#!/usr/bin/env python3
"""Collect host-owned GitHub, review, and release facts for the factory floor."""
import argparse
import importlib.util
import json
import re
import subprocess
import time
from pathlib import Path
from urllib.parse import urlparse


HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("factory_intake", HERE / "factory-intake.py")
intake = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(intake)
REVIEW_SPEC = importlib.util.spec_from_file_location("factory_review_intake", HERE / "factory-review-intake.py")
review = importlib.util.module_from_spec(REVIEW_SPEC)
REVIEW_SPEC.loader.exec_module(review)
FORMAL_SPEC = importlib.util.spec_from_file_location("factory_production_reviews", HERE / "factory-production-reviews.py")
formal = importlib.util.module_from_spec(FORMAL_SPEC)
FORMAL_SPEC.loader.exec_module(formal)

REPOSITORY = re.compile(r"^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$")
SHA = re.compile(r"^[0-9a-f]{40}$")
MAX_PRS = 100
MAX_RUNS = 100
MAX_JOBS = 32
MAX_JOB_READS = 8
MAX_JOURNAL = 4 << 20
MAX_LINKS = 256
MAX_DELIVERIES = 128
MAX_REVIEWERS = 256
MAX_DOCUMENT = 32768
MAX_INPUT = 240 << 10
MAINTENANCE_REPOSITORY = "dark-factory-build/dark-factory"


def text(value, limit=500):
    return value[:limit] if isinstance(value, str) else ""


def sha(value):
    return value if isinstance(value, str) and SHA.fullmatch(value) else ""


def number(value):
    return value if type(value) is int and value > 0 else None


def url(value, limit=1000):
    if not isinstance(value, str) or len(value) > limit:
        return ""
    parsed = urlparse(value)
    return value if parsed.scheme == "https" and parsed.netloc else ""


def milliseconds(value):
    return value * 1000 if type(value) is int and value > 0 else 0


def read_journal(path):
    """Read a configured local receipt without exposing its contents on failure."""
    try:
        if not isinstance(path, (str, Path)) or not Path(path).is_absolute():
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


def host_config(config):
    """Use the existing legacy host guard; customer routes have no gh fallback."""
    value = intake.validate_config(config)
    review.require_legacy_home(value)
    return value


def release_destination(config, release):
    destination = release.get("destination")
    if isinstance(destination, str) and destination and len(destination) <= 160:
        return destination
    verifier = release.get("verify_argv")
    if isinstance(verifier, list) and any(isinstance(arg, str) and arg.endswith("verify-live-runtime.py") for arg in verifier):
        return "runtime:" + str(Path(config["factory_home"]).resolve())
    if isinstance(verifier, list) and any(isinstance(arg, str) and arg.endswith("verify-live-site.py") for arg in verifier):
        return "site:app.darkfactory.build"
    return ""


def journal_paths(config):
    reviews = [{"path": config["journal"] + ".reviews.json", "repository": config["repository"]}]
    releases, unavailable = [], False
    values = config.get("release_configs", [])
    if not isinstance(values, list) or len(values) > 32:
        return reviews, releases, True
    for path in values:
        release, error = read_journal(path)
        destination = release_destination(config, release)
        if error or not isinstance(release.get("repository"), str) or not REPOSITORY.fullmatch(release["repository"]) \
                or not isinstance(release.get("journal"), str) or not Path(release["journal"]).is_absolute() or not destination:
            unavailable = True
            continue
        releases.append({"path": release["journal"], "repository": release["repository"], "destination": destination})
    return reviews, releases, unavailable


def github(repository, endpoint):
    raw = intake.command(["gh", "api", "repos/" + repository + endpoint,
                          "--method", "GET"], timeout=30)
    value = json.loads(raw)
    if not isinstance(value, (dict, list)):
        raise ValueError
    return value


def process_start(pid):
    try:
        result = subprocess.run(["/bin/ps", "-o", "lstart=", "-p", str(pid)], capture_output=True, text=True, timeout=5)
        value = result.stdout.strip()
        return value if result.returncode == 0 and value else ""
    except (OSError, subprocess.SubprocessError):
        return ""


def _maintenance_identity(value):
    if not isinstance(value, dict) or value.get("release") is not True:
        return None
    fields = {key: text(value.get(key), 256) for key in ("version", "source", "target", "build_id")}
    if not all(fields.values()):
        return None
    fields["release"] = True
    return fields


def _maintenance_binary(path):
    try:
        completed = subprocess.run([str(path), "--build-identity"], capture_output=True,
                                   text=True, timeout=15, env={})
        if completed.returncode or len(completed.stdout.encode()) > 4096:
            return None
        return _maintenance_identity(json.loads(completed.stdout))
    except (OSError, subprocess.SubprocessError, ValueError, json.JSONDecodeError):
        return None


def maintenance(config):
    """Observe the host release and runtime through existing read-only paths."""
    home = Path(config["factory_home"]).resolve()
    available = {"version": "", "url": "", "state": "unknown"}
    installed = {key: "" for key in ("version", "source", "target", "build_id")}
    installed.update({"release": False, "state": "unknown"})
    running = dict(installed)
    result = {"destination": "runtime:" + str(home), "available": available,
              "installed": installed, "running": running, "state": "unknown"}
    if config.get("repository") != MAINTENANCE_REPOSITORY:
        available["state"] = result["state"] = "unavailable"
        installed["state"] = running["state"] = "unavailable"
        return result
    try:
        release = github(MAINTENANCE_REPOSITORY, "/releases/latest")
        version = text(release.get("tag_name"), 128) if isinstance(release, dict) else ""
        release_url = url(release.get("html_url"), 512) if isinstance(release, dict) else ""
        if version and release_url:
            available.update({"version": version, "url": release_url, "state": "available"})
        else:
            available["state"] = "unavailable"
    except (intake.IntakeError, ValueError, json.JSONDecodeError, OSError):
        available["state"] = "unavailable"
    binary_root = Path(str(home) + ".service") / "bin" / "current"
    identities = []
    for name in ("factoryctl", "factoryd", "factory-runner"):
        binary = binary_root / name
        if not binary.is_file():
            installed["state"] = "unavailable"
            identities = []
            break
        identity = _maintenance_binary(binary)
        if identity is None:
            installed["state"] = "unknown"
            identities = []
            break
        identities.append(identity)
    if len(identities) == 3 and all(identity == identities[0] for identity in identities[1:]):
        installed.update(identities[0])
        installed["state"] = "verified"
    factoryctl = binary_root / "factoryctl"
    if installed["state"] == "unavailable" or not factoryctl.is_file():
        running["state"] = "unavailable"
    else:
        env = {"DARK_FACTORY_SOCKET": str(home / "runtimes" / "factory.sock"),
               "DARK_FACTORY_OPERATOR_TOKEN_FILE": str(home / "operator.token")}
        try:
            completed = subprocess.run([str(factoryctl), "web", "status"], capture_output=True,
                                       text=True, timeout=15, env=env)
            status = json.loads(completed.stdout) if not completed.returncode and len(completed.stdout.encode()) <= 4096 else None
            identity = _maintenance_identity(status.get("build")) if isinstance(status, dict) else None
            if identity is None:
                running["state"] = "unknown"
            else:
                running.update(identity)
                running["state"] = "ready" if status.get("ready") is True else "observed"
        except (OSError, subprocess.SubprocessError, ValueError, json.JSONDecodeError):
            running["state"] = "unknown"
    if available["state"] == "available" and installed["state"] == "verified" and running["state"] == "ready":
        result["state"] = "ready"
    elif "unavailable" in (available["state"], installed["state"], running["state"]):
        result["state"] = "unavailable"
    return result


def active_review(config, operation):
    pr, head = number(operation.get("pr")), sha(operation.get("head"))
    if pr is None or not head:
        return False
    path = Path(config["journal"]).parent / ("review-" + str(pr) + "-" + head) / "activity.json"
    activity, error = read_journal(path)
    if error or activity.get("finished_at") is not None:
        return False
    pid = number(activity.get("pid"))
    if pid is None or activity.get("operation") != operation.get("review_operation") or activity.get("pr") != pr \
            or activity.get("head") != head or activity.get("repository") != config["repository"] \
            or not isinstance(activity.get("process_start"), str):
        return False
    return process_start(pid) == activity["process_start"]


def review_receipts(paths, repository):
    reviewers, markers, merges, unavailable = [], {}, {}, False
    for entry in paths:
        if entry["repository"].casefold() != repository.casefold():
            unavailable = True
            continue
        journal, error = read_journal(entry["path"])
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
            state = outcome if known else "running" if operation.get("review_attempted") and active_review({"journal": entry["path"].removesuffix(".reviews.json"), "repository": repository}, operation) else "unknown"
            receipt = {"id": operation_id, "number": pr, "head": head,
                       "name": "host review", "provider": text(operation.get("provider"), 32) or "codex",
                       "state": state}
            reviewers.append(receipt)
            if known:
                markers[(pr, head)] = {"head": head, "state": outcome}
            merge = text(operation.get("merge_state"), 32)
            if merge:
                merges[(pr, head)] = merge
    return reviewers[:MAX_REVIEWERS], markers, merges, unavailable, max(0, len(reviewers) - MAX_REVIEWERS)


def release_receipts(paths, repository):
    deliveries, metadata_ranks, unavailable = {}, {}, False
    for entry in paths:
        if entry["repository"].casefold() != repository.casefold():
            unavailable = True
            continue
        journal, error = read_journal(entry["path"])
        if error or journal.get("version") != 1 or not isinstance(journal.get("releases"), dict):
            unavailable = True
            continue
        for sequence, receipt in enumerate(journal["releases"].values()):
            if not isinstance(receipt, dict):
                continue
            revision = sha(receipt.get("sha"))
            if not revision:
                continue
            verification = receipt.get("verification") if isinstance(receipt.get("verification"), dict) else {}
            verified = (receipt.get("state") == "verified" and verification.get("healthy") is True
                        and verification.get("sha") == revision)
            destination = entry["destination"]
            # A release receipt's exact SHA is durable even when its verifier
            # has no external deployment ID (the runtime verifier does not).
            # Keep this identity stable through later verification updates.
            identity = destination + ":release:" + revision
            key = (identity, destination, revision)
            delivery = deliveries.setdefault(key, {"id": identity, "kind": "release", "destination": destination,
                                                    "revision": revision, "state": "verified" if verified else "unknown",
                                                    "pull_requests": []})
            updated_at = milliseconds(receipt.get("updated_at"))
            verified_at = milliseconds(receipt.get("verified_at"))
            rank = (updated_at or verified_at, sequence)
            if key not in metadata_ranks or rank >= metadata_ranks[key]:
                metadata_ranks[key] = rank
                delivery["state"] = "verified" if verified else text(receipt.get("state"), 32) or "unknown"
                delivery["phase"] = text(receipt.get("phase"), 64)
                delivery["reason"] = text(receipt.get("error"), 2048)
                delivery["updated_at"] = updated_at
                receipt_url = url(verification.get("url")) or url(receipt.get("url"))
                if receipt_url:
                    delivery["url"] = receipt_url
                else:
                    delivery.pop("url", None)
                if verified and verified_at:
                    delivery["verified_at"] = verified_at
                else:
                    delivery.pop("verified_at", None)
            own_pr = number(receipt.get("pr"))
            superseded = receipt.get("superseded_by")
            own_pr_current = not isinstance(superseded, dict) or superseded.get("sha") == revision
            if own_pr is not None and own_pr_current and own_pr not in delivery["pull_requests"]:
                delivery["pull_requests"].append(own_pr)
            sources = receipt.get("included_pull_requests", receipt.get("delivery_sources"))
            if not isinstance(sources, list):
                sources = []
            for source in sources:
                source_pr = number(source.get("pr")) if isinstance(source, dict) else None
                source_repository = source.get("repository", entry["repository"]) if isinstance(source, dict) else ""
                if source_pr is not None and isinstance(source_repository, str) and source_repository.casefold() == repository.casefold() and source_pr not in delivery["pull_requests"]:
                    delivery["pull_requests"].append(source_pr)
    for delivery in deliveries.values():
        delivery["overflow"] = max(0, len(delivery["pull_requests"]) - MAX_LINKS)
        delivery["pull_requests"] = delivery["pull_requests"][:MAX_LINKS]
        if not delivery["overflow"]:
            del delivery["overflow"]
    return list(deliveries.values())[:MAX_DELIVERIES], unavailable, max(0, len(deliveries) - MAX_DELIVERIES)


def pull_requests(value, markers, queues):
    if not isinstance(value, list):
        raise ValueError
    result, heads = [], {}
    for item in value:
        if not isinstance(item, dict):
            continue
        pr, head = number(item.get("number")), sha((item.get("head") or {}).get("sha") if isinstance(item.get("head"), dict) else "")
        if pr is None or not head:
            continue
        branch = text(item["head"].get("ref"), 240)
        review = markers.get((pr, head), {"head": head, "state": "unknown"})
        record = {"number": pr, "title": text(item.get("title")), "url": url(item.get("html_url")),
                  "head": head, "branch": branch, "base": text((item.get("base") or {}).get("ref") if isinstance(item.get("base"), dict) else "", 240),
                  "state": text(item.get("state"), 32) or "unknown", "review": review}
        merge = sha(item.get("merge_commit_sha"))
        if merge:
            merged_at = text(item.get("merged_at"), 64)
            if merged_at:
                record.update({"state": "merged", "merge": merge, "merged_at": merged_at})
        queue = queues.get((pr, head), "")
        if queue:
            record["merge_queue"] = queue
        result.append(record)
        heads.setdefault(head, []).append(pr)
    return result, heads


def run_pull_requests(run, heads):
    linked = set(heads.get(sha(run.get("head_sha")), []))
    values = run.get("pull_requests")
    if isinstance(values, list):
        for item in values:
            pr = number(item.get("number")) if isinstance(item, dict) else None
            if pr is not None:
                linked.add(pr)
    linked = sorted(linked)
    return linked[:MAX_LINKS], max(0, len(linked) - MAX_LINKS)


def checks(repository, value, heads, current_heads):
    if not isinstance(value, dict) or not isinstance(value.get("workflow_runs"), list):
        raise ValueError
    result, seen, unavailable, job_reads = [], set(), 0, 0
    for run in value["workflow_runs"][:MAX_RUNS]:
        if not isinstance(run, dict) or number(run.get("id")) is None:
            continue
        run_id = str(run["id"])
        if run_id in seen:
            continue
        seen.add(run_id)
        linked, link_overflow = run_pull_requests(run, heads)
        if not linked:
            continue
        jobs, omitted = [], 0
        priority = sha(run.get("head_sha")) in current_heads or run.get("event") == "merge_group"
        if priority and job_reads < MAX_JOB_READS:
            job_reads += 1
            try:
                response = github(repository, "/actions/runs/" + run_id + "/jobs?per_page=" + str(MAX_JOBS))
                values = response.get("jobs", [])
                if not isinstance(values, list):
                    raise ValueError
                omitted = max(0, int(response.get("total_count", len(values))) - MAX_JOBS)
                for job in values[:MAX_JOBS]:
                    if not isinstance(job, dict) or number(job.get("id")) is None:
                        continue
                    jobs.append({"id": str(job["id"]), "name": text(job.get("name"), 256),
                                 "state": text(job.get("status"), 32) or "unknown",
                             "conclusion": text(job.get("conclusion"), 32), "url": url(job.get("html_url"), 256)})
            except (intake.IntakeError, ValueError, json.JSONDecodeError):
                unavailable += 1
        else:
            # A skipped job page is explicitly incomplete, never an empty pass.
            omitted, unavailable = 1, unavailable + 1
        check = {"id": run_id, "name": text(run.get("name"), 256), "revision": sha(run.get("head_sha")),
                 "scope": "merge_group" if run.get("event") == "merge_group" else "head", "state": text(run.get("status"), 32) or "unknown",
                 "conclusion": text(run.get("conclusion"), 32), "url": url(run.get("html_url"), 512),
                 "pull_requests": linked, "jobs": jobs}
        if omitted or link_overflow:
            check["overflow"] = omitted + link_overflow
        result.append(check)
    total = value.get("total_count")
    run_overflow = max(0, total - MAX_RUNS) if type(total) is int else (1 if len(value["workflow_runs"]) >= MAX_RUNS else 0)
    return result, run_overflow, unavailable


def collect(config):
    """Return one read-only ProductionObservation-compatible mapping."""
    repository = config.get("repository") if isinstance(config, dict) else ""
    if not isinstance(repository, str) or not REPOSITORY.fullmatch(repository):
        raise ValueError("repository must be OWNER/REPOSITORY")
    try:
        config = host_config(config)
    except (ValueError, intake.IntakeError, review.ReviewError, OSError):
        return {"repository": repository, "observed_at": int(time.time() * 1000), "pull_requests": [], "checks": [], "reviewers": [], "deliveries": [], "unavailable": "host_controller_only"}
    review_paths, release_paths, path_unavailable = journal_paths(config)
    reviewers, markers, queues, review_unavailable, reviewer_overflow = review_receipts(review_paths, config["repository"])
    formal_overflow, formal_unavailable = 0, ""
    try:
        formal_reviews, formal_queues, formal_overflow, formal_unavailable = formal.collect(config["repository"])
        markers.update(formal_reviews)
        queues.update(formal_queues)
    except (intake.IntakeError, formal.intake.IntakeError, ValueError, json.JSONDecodeError, OSError):
        formal_unavailable = "formal_reviews"
        markers = {} # An old controller ALLOW cannot overrule an unseen formal BLOCK.
    deliveries, release_unavailable, delivery_overflow = release_receipts(release_paths, config["repository"])
    observation = {"repository": config["repository"], "observed_at": int(time.time() * 1000), "pull_requests": [], "checks": [],
                   "reviewers": reviewers, "deliveries": deliveries, "maintenance": maintenance(config)}
    unavailable = []
    if formal_unavailable:
        unavailable.append("formal_reviews")
    if review_unavailable:
        unavailable.append("review_journal")
    if release_unavailable:
        unavailable.append("release_journal")
    if path_unavailable:
        unavailable.append("release_config")
    try:
        open_prs = github(config["repository"], "/pulls?state=open&per_page=" + str(MAX_PRS))
        closed_prs = github(config["repository"], "/pulls?state=closed&sort=updated&direction=desc&per_page=" + str(MAX_PRS))
        if not isinstance(open_prs, list) or not isinstance(closed_prs, list):
            raise ValueError
        open_records, current_heads = pull_requests(open_prs, markers, queues)
        closed_records, closed_heads = pull_requests(closed_prs, markers, queues)
        heads = dict(current_heads)
        for revision, numbers in closed_heads.items():
            heads.setdefault(revision, []).extend(number for number in numbers if number not in heads[revision])
        observation["pull_requests"] = open_records + closed_records
        pr_overflow = int(len(open_prs) >= MAX_PRS) + int(len(closed_prs) >= MAX_PRS)
        runs = github(config["repository"], "/actions/runs?per_page=" + str(MAX_RUNS))
        observation["checks"], run_overflow, job_unavailable = checks(config["repository"], runs, heads, current_heads)
        observation["overflow"] = pr_overflow + run_overflow + delivery_overflow + formal_overflow + reviewer_overflow
        if job_unavailable:
            unavailable.append("jobs")
    except (intake.IntakeError, ValueError, json.JSONDecodeError):
        unavailable.append("github")
    if observation.get("overflow") == 0:
        observation.pop("overflow", None)
    if unavailable:
        observation["unavailable"] = ",".join(unavailable)
    return observation


def record(config, observation):
    try:
        config = host_config(config)
    except (ValueError, intake.IntakeError, review.ReviewError, OSError):
        return False
    home = Path(config["factory_home"])
    factoryctl = Path(str(home) + ".service") / "bin" / "current" / "factoryctl"
    if not factoryctl.is_file():
        return False
    env = {"DARK_FACTORY_SOCKET": str(home / "runtimes" / "factory.sock"),
           "DARK_FACTORY_OPERATOR_TOKEN_FILE": str(home / "operator.token")}
    common = {key: observation.get(key) for key in ("repository", "observed_at", "unavailable", "overflow", "maintenance") if key in observation}
    batches, batch = [], dict(common, pull_requests=[], checks=[], reviewers=[], deliveries=[])
    for name in ("pull_requests", "checks", "reviewers", "deliveries"):
        for item in observation.get(name, []):
            candidate = dict(batch, **{name: batch[name] + [item]})
            document = json.dumps({"project_id": config["project_id"], "observation": candidate})
            if len(document.encode()) > MAX_INPUT:
                if not any(batch[key] for key in ("pull_requests", "checks", "reviewers", "deliveries")):
                    return False
                batches.append(batch)
                batch = dict(common, pull_requests=[], checks=[], reviewers=[], deliveries=[])
                candidate = dict(batch, **{name: [item]})
                if len(json.dumps({"project_id": config["project_id"], "observation": candidate}).encode()) > MAX_INPUT:
                    return False
            batch = candidate
    batches.append(batch)
    try:
        for batch in batches:
            document = json.dumps({"project_id": config["project_id"], "observation": batch})
            completed = subprocess.run([str(factoryctl), "production", "observe", "--json-stdin"],
                                       input=document, text=True, capture_output=True, timeout=30, env=env)
            if completed.returncode:
                return False
        return True
    except (OSError, subprocess.TimeoutExpired):
        return False


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("config", type=Path)
    parser.add_argument("--record", action="store_true")
    args = parser.parse_args(argv)
    config = json.loads(args.config.read_text(encoding="utf-8"))
    observation = collect(config)
    if args.record and not record(config, observation):
        print("factory-production: record unavailable", file=__import__("sys").stderr)
        raise SystemExit(1)
    print(json.dumps(observation, sort_keys=True))


if __name__ == "__main__":
    main()
