#!/usr/bin/env python3
"""Read formal GitHub reviews for the production observation projection.

This module has no publication path.  It reads the first 100 open or recently
merged pull requests in one GraphQL request and returns bounded, exact-head
review facts.
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
MAX_GATE_BODY_BYTES = 65536
MAX_FINDINGS_BYTES = 8192

QUERY = """
query($owner:String!, $name:String!) {
  repository(owner:$owner, name:$name) {
    pullRequests(first:100, states:[OPEN, MERGED], orderBy:{field:UPDATED_AT, direction:DESC}) {
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
        if not isinstance(commit, str) or not SHA.fullmatch(commit):
            unavailable.append("review_commit")
            continue
        author = review.get("author")
        actor = author.get("databaseId") if isinstance(author, dict) else None
        if type(actor) is not int or actor < 1:
            unavailable.append("review_actor")
            continue
        state = review.get("state")
        if state not in {"APPROVED", "CHANGES_REQUESTED", "COMMENTED"}:
            continue
        body = review.get("body")
        if not isinstance(body, str) or len(body.encode("utf-8", "replace")) > MAX_GATE_BODY_BYTES:
            unavailable.append("review_body")
            continue
        facts.append({"commit_id": commit, "state": state, "author_id": str(actor),
                      "body": body, "url": review.get("url") if isinstance(review.get("url"), str) else ""})
    return facts, truncated


def collect(repository):
    """Return ``(reviews, queues, overflow, unavailable)`` for current PRs.

    Open and recently merged PRs are returned.  ``reviews`` is keyed by
    ``(number, head)`` and each value contains only
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
        reasons = []
        facts, truncated = _review_facts(pr.get("reviews"), number, head, reasons)
        unavailable.extend(reasons)
        if truncated:
            overflow += 1
        current_facts = [fact for fact in facts if fact["commit_id"] == head]
        result = verify_exact_head(head, facts)
        blocking = "blocking verdict(s)" in (result.stderr or "")
        if blocking and (not truncated and not reasons or any(fact["state"] == "CHANGES_REQUESTED" for fact in current_facts)):
            state = "block"
        elif result.returncode == 0 and not truncated and not reasons:
            state = "allow"
        else:
            state = "unknown"
        selected = next((fact for fact in reversed(current_facts) if fact["state"] == "CHANGES_REQUESTED"), None)
        if selected is None and state == "allow":
            selected = next((fact for fact in reversed(current_facts) if "Dark-Factory-Review: allow " + head in fact["body"]), None)
        selected = selected or (current_facts[-1] if current_facts else {"url": "", "body": ""})
        reviews[key] = {"head": head, "state": state, "url": selected["url"], "findings": _bounded_text(selected["body"])}
    return reviews, queues, overflow, ",".join(sorted(set(unavailable)))


def review_lines(head, reviews):
    """Encode collector facts for the existing exact-head review verifier."""
    lines = []
    for fact in reviews:
        body = str(fact.get("body", "")).replace("\t", " ").replace("\r", " ").replace("\n", " ")
        lines.append("\t".join((fact.get("commit_id", head), fact.get("state", ""), fact.get("author_id", ""), body)))
    return "\n".join(lines) + ("\n" if lines else "")


def verify_exact_head(head, review_records, verifier=None):
    """Run the repository's shared offline verdict policy without reimplementing it."""
    if not SHA.fullmatch(head):
        raise ValueError("head must be a commit SHA")
    verifier = verifier or str(HERE / "verify-adversarial-review.sh")
    with tempfile.NamedTemporaryFile("w", encoding="utf-8") as stream:
        stream.write(review_lines(head, review_records))
        stream.flush()
        env = {"PATH": "/usr/bin:/bin", "DF_REVIEW_HEAD_SHA": head, "DF_REVIEW_REVIEWS": stream.name}
        return subprocess.run([verifier], env=env, check=False, capture_output=True, text=True)


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("repository")
    args = parser.parse_args()
    found, queues, overflow, unavailable = collect(args.repository)
    print(json.dumps({"reviews": {f"{number}:{head}": value for (number, head), value in found.items()}, "queues": {f"{number}:{head}": value for (number, head), value in queues.items()}, "overflow": overflow, "unavailable": unavailable}, sort_keys=True))
