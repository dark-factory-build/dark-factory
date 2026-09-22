#!/usr/bin/env python3
"""Collect host-owned release and maintenance facts for the factory floor."""
import argparse
import hashlib
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

REPOSITORY = re.compile(r"^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$")
SHA = re.compile(r"^[0-9a-f]{40}$")
MAX_JOURNAL = 4 << 20
MAX_LINKS = 256
MAX_DELIVERIES = 128
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


def runtime_destination(home):
    digest = hashlib.sha256(str(Path(home).resolve()).encode("utf-8")).hexdigest()[:16]
    return "runtime:host-" + digest


def release_destination(config, release):
    destination = release.get("destination")
    if isinstance(destination, str) and destination and len(destination) <= 160:
        return destination
    verifier = release.get("verify_argv")
    if isinstance(verifier, list) and any(isinstance(arg, str) and arg.endswith("verify-live-runtime.py") for arg in verifier):
        return runtime_destination(config["factory_home"])
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
    result = {"destination": runtime_destination(home), "available": available,
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
            # A journal order is not proof of recency when second-resolution times tie.
            rank = (updated_at or verified_at, not verified, sequence)
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


def collect(config):
    """Return one read-only ProductionObservation-compatible mapping."""
    repository = config.get("repository") if isinstance(config, dict) else ""
    if not isinstance(repository, str) or not REPOSITORY.fullmatch(repository):
        raise ValueError("repository must be OWNER/REPOSITORY")
    try:
        config = host_config(config)
    except (ValueError, intake.IntakeError, review.ReviewError, OSError):
        return {"repository": repository, "observed_at": int(time.time() * 1000), "pull_requests": [], "checks": [], "reviewers": [], "deliveries": [], "unavailable": "host_controller_only"}
    _review_paths, release_paths, path_unavailable = journal_paths(config)
    deliveries, release_unavailable, delivery_overflow = release_receipts(release_paths, config["repository"])
    observation = {"repository": config["repository"], "observed_at": int(time.time() * 1000),
                   "pull_requests": [], "checks": [], "reviewers": [], "deliveries": deliveries,
                   "maintenance": maintenance(config)}
    unavailable = []
    if release_unavailable:
        unavailable.append("release_journal")
    if path_unavailable:
        unavailable.append("release_config")
    observation["overflow"] = delivery_overflow
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
