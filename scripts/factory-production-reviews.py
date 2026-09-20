#!/usr/bin/env python3
"""Read formal GitHub reviews for the production observation projection.

This module has no publication path.  It reads the first 100 open pull
requests in one GraphQL request and returns bounded, exact-head review facts.
The existing ``verify-adversarial-review.sh`` remains the verdict policy.
"""
import importlib.util
import json
import re
import subprocess
import tempfile
from pathlib import Path


HERE = Path(__file__).resolve().parent
INTAKE_SPEC = importlib.util.spec_from_file_location("factory_intake", HERE / "factory-intake.py")
intake = importlib.util.module_from_spec(INTAKE_SPEC)
INTAKE_SPEC.loader.exec_module(intake)

REPOSITORY = re.compile(r"^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$")
SHA = re.compile(r"^[0-9a-f]{40}$")
MAX_PULL_REQUESTS = 100
MAX_REVIEWS = 32
MAX_FINDINGS_BYTES = 8192

QUERY = """
query($owner:String!, $name:String!) {
  repository(owner:$owner, name:$name) {
    pullRequests(first:100, states:OPEN) {
      pageInfo { hasNextPage }
      nodes {
        number
        headRefOid
        mergeQueueEntry { state }
        reviews(last:32) {
          pageInfo { hasPreviousPage }
          nodes {
            commit { oid }
            state
            body
            url
            author {
              ... on User { databaseId }
              ... on Bot { databaseId }
            }
          }
        }
      }
    }
  }
}
"""


def _bounded_text(value, limit=8192):
    if not isinstance(value, str):
        return ""
    raw = value.encode("utf-8", "replace")[:limit]
    return raw.decode("utf-8", "ignore")


def _repository_parts(repository):
    if not isinstance(repository, str) or not REPOSITORY.fullmatch(repository):
        raise ValueError("repository must be OWNER/REPOSITORY")
    return repository.split("/", 1)


def _read_graphql(repository):
    owner, name = _repository_parts(repository)
    encoded = intake.command([
        "gh", "api", "graphql", "-f", "query=" + QUERY,
        "-F", "owner=" + owner, "-F", "name=" + name,
    ], timeout=30)
    value = json.loads(encoded)
    if not isinstance(value, dict) or value.get("errors"):
        raise ValueError("GitHub review query failed")
    pull_requests = ((value.get("data") or {}).get("repository") or {}).get("pullRequests")
    if not isinstance(pull_requests, dict) or not isinstance(pull_requests.get("nodes"), list):
        raise ValueError("GitHub review query returned an invalid shape")
    return pull_requests


def _review_facts(nodes, number, head, unavailable):
    facts = []
    truncated = False
    if not isinstance(nodes, dict) or not isinstance(nodes.get("nodes"), list):
        return facts, True
    page = nodes.get("pageInfo")
    truncated = not isinstance(page, dict) or page.get("hasPreviousPage") is True
    for review in nodes["nodes"][-MAX_REVIEWS:]:
        if not isinstance(review, dict):
            unavailable.append("review_shape")
            continue
        commit = (review.get("commit") or {}).get("oid")
        if not SHA.fullmatch(commit or "") or commit != head:
            continue
        author = review.get("author")
        actor = author.get("databaseId") if isinstance(author, dict) else None
        if type(actor) is not int or actor < 1:
            unavailable.append("review_actor")
            continue
        state = review.get("state") if review.get("state") in {"APPROVED", "CHANGES_REQUESTED", "COMMENTED"} else "COMMENTED"
        facts.append({"state": state, "url": review.get("url") if isinstance(review.get("url"), str) else "", "findings": _bounded_text(review.get("body"))})
    return facts, truncated


def collect(repository):
    """Return ``(reviews, queues, overflow, unavailable)`` for open PRs.

    ``reviews`` is keyed by ``(number, head)`` and each value contains only
    ``head``, ``state``, ``url`` and bounded ``findings``.  Missing review
    history is ``unknown``; a present ``CHANGES_REQUESTED`` review remains a
    proven block even when older reviews were truncated.  ``unavailable`` is a
    list of fail-closed query/shape reasons.
    """
    pull_requests = _read_graphql(repository)
    reviews, queues, unavailable = {}, {}, []
    overflow = 1 if pull_requests.get("pageInfo", {}).get("hasNextPage") else 0
    nodes = pull_requests.get("nodes", [])
    if len(nodes) > MAX_PULL_REQUESTS:
        overflow += len(nodes) - MAX_PULL_REQUESTS
    for pr in nodes[:MAX_PULL_REQUESTS]:
        if not isinstance(pr, dict) or type(pr.get("number")) is not int or pr["number"] < 1 or not SHA.fullmatch(pr.get("headRefOid") or ""):
            unavailable.append("pull_request_shape")
            continue
        number, head = pr["number"], pr["headRefOid"]
        key = (number, head)
        queue = (pr.get("mergeQueueEntry") or {}).get("state")
        if isinstance(queue, str) and queue:
            queues[key] = queue[:64]
        facts, truncated = _review_facts(pr.get("reviews"), number, head, unavailable)
        if truncated:
            overflow += 1
        if any(fact["state"] == "CHANGES_REQUESTED" for fact in facts):
            selected = next(fact for fact in facts if fact["state"] == "CHANGES_REQUESTED")
            state = "block"
        elif truncated:
            selected = facts[-1] if facts else {"url": "", "findings": ""}
            state = "unknown"
        elif facts:
            selected = facts[-1]
            state = "allow" if selected["state"] == "APPROVED" else "note"
        else:
            selected = {"url": "", "findings": ""}
            state = "unknown"
        reviews[key] = {"head": head, "state": state, "url": selected["url"], "findings": _bounded_text(selected["findings"])}
    return reviews, queues, overflow, ",".join(sorted(set(unavailable)))


def review_lines(head, reviews):
    """Encode collector facts for the existing exact-head review verifier."""
    lines = []
    for fact in reviews:
        body = str(fact.get("findings", "")).replace("\t", " ").replace("\r", " ").replace("\n", " ")
        state = {"block": "CHANGES_REQUESTED", "allow": "APPROVED"}.get(fact.get("state"), "COMMENTED")
        lines.append("\t".join((fact.get("head", head), state, "1", body)))
    return "\n".join(lines) + ("\n" if lines else "")


def verify_exact_head(head, review_records, verifier=None):
    """Run the repository's shared offline verdict policy without reimplementing it."""
    if not SHA.fullmatch(head):
        raise ValueError("head must be a commit SHA")
    verifier = verifier or str(HERE / "verify-adversarial-review.sh")
    with tempfile.NamedTemporaryFile("w", encoding="utf-8") as stream:
        stream.write(review_lines(head, review_records))
        stream.flush()
        env = dict(__import__("os").environ, DF_REVIEW_HEAD_SHA=head, DF_REVIEW_REVIEWS=stream.name)
        return subprocess.run([verifier], env=env, check=False, capture_output=True, text=True)


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("repository")
    args = parser.parse_args()
    found, queues, overflow, unavailable = collect(args.repository)
    print(json.dumps({"reviews": {f"{number}:{head}": value for (number, head), value in found.items()}, "queues": {f"{number}:{head}": value for (number, head), value in queues.items()}, "overflow": overflow, "unavailable": unavailable}, sort_keys=True))
