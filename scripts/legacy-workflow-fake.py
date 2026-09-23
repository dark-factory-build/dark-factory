#!/usr/bin/env python3
"""Disposable external Codex, GitHub CLI, and Maintainer App for the legacy E2E.

The program is installed under three names in a private tools directory.  It
does not drive a controller pass: only the factory provider and the scheduled
controllers invoke it.  Its one JSON file represents external GitHub state.
"""
import fcntl
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import traceback
import uuid

ROOT = Path(sys.argv[0]).absolute().parent.parent
STATE = ROOT / "external.json"
GIT = "/Library/Developer/CommandLineTools/usr/bin/git"
REPO = "fixture/legacy"


def git(*args, cwd=None):
    return subprocess.check_output([GIT, *map(str, args)], cwd=cwd, text=True).strip()


def transaction(action):
    with (ROOT / "external.lock").open("a+") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        state = json.loads(STATE.read_text())
        value = action(state)
        temporary = STATE.with_suffix(".next")
        temporary.write_text(json.dumps(state, sort_keys=True))
        os.replace(temporary, STATE)
        return value


def response(value):
    print(json.dumps(value, separators=(",", ":")))


def operation(state, name, arguments, result):
    key = arguments["operation_id"].lower()
    digest = hashlib.sha256(json.dumps(arguments, separators=(",", ":")).encode()).hexdigest()
    state["operations"][key] = {"operation_id": key, "state": "completed", "kind": name,
                                "request_digest": digest, "result": result}
    return digest


def marker(key, digest):
    return f"\n\n<!-- dark-factory-operation:{key.lower()}:{digest} -->"


def reported_conflict(state, pr):
    attempts = state["worker_attempts"].get("7", [])
    return state["case"] == "conflict" and pr["number"] == state["source_pr"].get("7") and \
        bool(attempts) and pr["head"] == attempts[0]["head"]


def app(state, name, args):
    if name == "observe_operation":
        if state["case"] == "lost-response" and args["operation_id"] == state.get("lost_create_operation"):
            state["lost_create_observations"] = state.get("lost_create_observations", 0) + 1
        return state["operations"].get(args["operation_id"].lower(),
                                       {"operation_id": args["operation_id"].lower(), "state": "missing"})
    if name == "observe_pull_request_merge":
        pr = state["prs"][str(args["pull_number"])]
        if pr["head"] != args["head_sha"]:
            raise ValueError("merge observation head changed")
        return {"pull_number": pr["number"], "head_sha": pr["head"], "base": "main",
                "pull_state": "closed" if pr["state"] == "closed" else "open",
                "state": "MERGED_AFTER_ENQUEUE_ATTEMPT" if pr["state"] == "closed" else "ACTIVE_QUEUE",
                "entry_id": f"fixture-entry-{pr['number']}", "queue_state": "MERGED" if pr["state"] == "closed" else "QUEUED",
                "merge_commit_sha": pr.get("merge_sha")}
    if name == "observe_pull_request_review":
        pr = state["prs"][str(args["pull_number"])]
        return next(review for review in pr["reviews"] if review["id"] == args["review_id"])
    if name == "create_issue":
        number = 8
        result = {"number": number, "url": f"https://github.com/{REPO}/issues/{number}"}
        digest = operation(state, name, args, result)
        state["issues"][str(number)] = {"number": number, "title": args["title"],
            "body": args["body"] + marker(args["operation_id"], digest), "state": "OPEN",
            "author": {"login": "fixture-app"}, "labels": [], "updatedAt": "2026-09-23T00:00:00Z",
            "url": result["url"]}
        return result
    if name == "publish_commit":
        branch = args["branch"]
        source_git = state["git_dirs"][branch]
        sha = git("--git-dir", source_git, "rev-parse", branch)
        git("--git-dir", source_git, "push", "-q", str(ROOT / "github.git"),
            f"{branch}:refs/heads/{branch}")
        result = {"branch": branch, "commit_sha": sha, "parent_sha": args["expected_head_sha"]}
        operation(state, name, args, result)
        state["published"][branch] = sha
        return result
    if name == "create_pull_request":
        issue = str(args["issue_number"])
        state.setdefault("create_count", {})[issue] = state.setdefault("create_count", {}).get(issue, 0) + 1
        number = len(state["prs"]) + 1
        branch = args["head"]
        head = state["published"][branch]
        result = {"number": number, "url": f"https://github.com/{REPO}/pull/{number}",
                  "head_sha": head, "base_sha": args["base_sha"]}
        digest = operation(state, name, args, result)
        stale = []
        if state["case"] in ("ci", "conflict") and args["issue_number"] == 7:
            # A separate GitHub actor approved the predecessor head. It has
            # no App operation and must never authorize the corrected head.
            stale = [{"id": 1, "commit_id": head, "state": "COMMENTED",
                      "body": "Dark-Factory-Review: allow " + head,
                      "user": {"id": 202, "login": "fixture-stale-reviewer"}}]
        state["prs"][str(number)] = {"number": number, "head": head, "branch": branch,
            "body": args["body"] + f"\n\nRefs #{args['issue_number']}" + marker(args["operation_id"], digest),
            "state": "open", "reviews": stale,
            "base": "main", "title": args["title"], "queue": False}
        git("--git-dir", ROOT / "github.git", "update-ref", f"refs/pull/{number}/head", head)
        return result
    if name == "update_pull_request_body":
        pr = state["prs"][str(args["pull_number"])]
        result = {"number": pr["number"], "url": f"https://github.com/{REPO}/pull/{pr['number']}"}
        digest = operation(state, name, args, result)
        pr["body"] = args["body"] + marker(args["operation_id"], digest)
        pr["head"] = state["published"][pr["branch"]]
        git("--git-dir", ROOT / "github.git", "update-ref", f"refs/pull/{pr['number']}/head", pr["head"])
        return result
    if name == "submit_pull_request_review":
        pr = state["prs"][str(args["pull_number"])]
        verdict = {"REQUEST_CHANGES": "block", "ALLOW": "allow"}.get(args["event"])
        if verdict is None or args["head_sha"] != pr["head"]:
            raise ValueError("invalid exact-head reviewer verdict")
        rid = len(pr["reviews"]) + 1
        result = {"review_id": rid, "url": f"https://github.com/{REPO}/pull/{pr['number']}#pullrequestreview-{rid}",
                  "head_sha": args["head_sha"], "verdict": verdict}
        digest = operation(state, name, args, result)
        body = "Fix the first draft" if verdict == "block" else "Correction verified"
        body += f"\nDark-Factory-Review: {verdict} {args['head_sha']}"
        if args.get("corrects_review_operation_id"):
            body += "\nDark-Factory-Review-Correction: " + args["corrects_review_operation_id"]
        body += marker(args["operation_id"], digest)
        pr["reviews"].append({"id": rid, "commit_id": args["head_sha"],
            "state": "COMMENTED", "body": body,
            "user": {"id": 101, "login": "fixture-app"}})
        return result
    if name == "enqueue_pull_request":
        pr = state["prs"][str(args["pull_number"])]
        if args["head_sha"] != pr["head"]:
            raise ValueError("stale head")
        verdicts = [review for review in pr["reviews"] if review["commit_id"] == pr["head"]]
        if not verdicts or verdicts[-1]["state"] != "COMMENTED" or \
                f"Dark-Factory-Review: allow {pr['head']}" not in verdicts[-1]["body"]:
            raise ValueError("exact head has no independent ALLOW")
        if args.get("reviewed_body_digest") != "sha256:" + hashlib.sha256(pr["body"].encode()).hexdigest():
            raise ValueError("reviewed body changed before queue entry")
        pr["queue"] = True
        operation(state, name, args, {"pull_number": pr["number"], "head_sha": pr["head"], "state": "queued"})
        # GitHub's merge queue is the external actor. The product merely asks
        # the App to enqueue; this response models the later external merge.
        with tempfile.TemporaryDirectory(dir=ROOT) as work:
            git("clone", "-q", str(ROOT / "github.git"), work)
            git("-c", "user.name=fixture-queue", "-c", "user.email=queue@invalid",
                "merge", "-q", "--no-ff", "-m", "merge queued PR", pr["head"], cwd=work)
            merge_sha = git("rev-parse", "HEAD", cwd=work)
            git("push", "-q", "origin", "HEAD:refs/heads/main", cwd=work)
        pr["merge_sha"] = merge_sha
        pr["state"] = "closed"
        state["main"] = merge_sha
        return {"pull_number": pr["number"], "head_sha": pr["head"], "state": "queued"}
    raise ValueError("unsupported fixture App tool: " + name)


def bridge():
    for line in sys.stdin:
        request = json.loads(line)
        if request["method"] == "initialize":
            response({"jsonrpc": "2.0", "id": request.get("id"), "result": {"protocolVersion": "2024-11-05", "capabilities": {"tools": {}}, "serverInfo": {"name": "fixture-maintainer", "version": "1"}}})
        elif request["method"] == "tools/list":
            response({"jsonrpc": "2.0", "id": request["id"], "result": {"tools": [{"name": name, "inputSchema": {"type": "object"}} for name in ("observe_operation", "observe_pull_request_merge", "observe_pull_request_review", "create_issue", "publish_commit", "create_pull_request", "update_pull_request_body", "submit_pull_request_review", "enqueue_pull_request")]}})
        elif request["method"] == "tools/call":
            name, args = request["params"]["name"], request["params"]["arguments"]
            try:
                value = transaction(lambda state: app(state, name, args))
                response({"jsonrpc": "2.0", "id": request["id"], "result": {"structuredContent": value, "isError": False, "content": [{"type": "text", "text": json.dumps(value)}]}})
            except Exception as exc:
                response({"jsonrpc": "2.0", "id": request["id"], "result": {"isError": True, "content": [{"type": "text", "text": str(exc)}]}})


def gh():
    args = sys.argv[1:]
    state = json.loads(STATE.read_text())
    if args[:2] == ["issue", "list"]:
        return response([{"number": 7}])
    if args[:2] == ["issue", "view"]:
        return response(state["issues"][args[2]])
    if args[:2] == ["pr", "view"]:
        pr = state["prs"][args[2]]
        return response({"body": pr["body"], "headRefOid": pr["head"],
                         "state": "MERGED" if pr["state"] == "closed" else "OPEN",
                         "baseRefName": "main", "mergeCommit": {"oid": pr["merge_sha"]} if pr["state"] == "closed" else None})
    if args and args[0] == "api":
        endpoint = args[1].split("?", 1)[0]
        if "/commits/" in endpoint and endpoint.endswith("/pulls"):
            sha = endpoint.split("/commits/", 1)[1].split("/pulls", 1)[0]
            pulls = [{"number": pr["number"], "base": {"ref": "main"}, "merge_commit_sha": pr["merge_sha"],
                      "merged_at": "2026-09-23T00:00:00Z"} for pr in state["prs"].values()
                     if pr["state"] == "closed" and pr["merge_sha"] == sha]
            return response([pulls] if "--slurp" in args else pulls)
        if endpoint.endswith("/pulls"):
            closed = "state=closed" in args[1]
            return response([{"number": pr["number"], "title": pr["title"],
                              "body": pr["body"], "headRefOid": pr["head"],
                              "head": {"sha": pr["head"], "ref": pr["branch"], "repo": {"full_name": REPO}},
                              "base": {"ref": "main", "sha": state["seed"]}, "state": pr["state"],
                              "mergeable": not reported_conflict(state, pr),
                              "mergeStateStatus": "DIRTY" if reported_conflict(state, pr) else "CLEAN",
                              "mergeable_state": "dirty" if reported_conflict(state, pr) else "clean",
                              "merge_commit_sha": pr.get("merge_sha") if pr["state"] == "closed" else None,
                              "merged_at": "2026-09-23T00:00:00Z" if pr["state"] == "closed" else None,
                              "html_url": f"https://github.com/{REPO}/pull/{pr['number']}"}
                             for pr in state["prs"].values() if (pr["state"] == "closed") == closed])
        match = re.search(r"/pulls/(\d+)(?:/(reviews))?$", endpoint)
        if match:
            pr = state["prs"][match[1]]
            if match[2]:
                return response(pr["reviews"])
            return response({"number": pr["number"], "head": {"sha": pr["head"]},
                             "mergeable": not reported_conflict(state, pr),
                             "mergeable_state": "dirty" if reported_conflict(state, pr) else "clean", "body": pr["body"]})
        if "/git/ref/heads/main" in endpoint:
            return response({"object": {"sha": state["main"]}})
        if endpoint.endswith("/check-runs"):
            page = {"check_runs": [{"id": 1, "name": "fixture-required", "status": "completed", "conclusion": "success", "app": {"id": 1}}]}
            return response([page] if "--slurp" in args else page)
        if endpoint.endswith("/actions/runs"):
            return response({"total_count": 0, "workflow_runs": []})
        if "/compare/" in endpoint:
            old, new = endpoint.rsplit("/compare/", 1)[1].split("...", 1)
            commits = [{"sha": sha} for sha in git("--git-dir", ROOT / "github.git", "rev-list", "--reverse", old + ".." + new).splitlines() if sha]
            return response({"status": "ahead" if commits else "identical", "total_commits": len(commits),
                             "merge_base_commit": {"sha": old}, "commits": commits})
    raise ValueError("unsupported fixture gh call: " + repr(args))


def provider():
    args = sys.argv[1:]
    if args and args[0] == "exec":
        output = Path(args[args.index("--output-last-message") + 1])
        state = json.loads(STATE.read_text())
        prompt = args[-1]
        pr = int(re.search(r"pull request #(\d+)", prompt)[1])
        head = re.search(r"exact head commit ([0-9a-f]{40})", prompt)[1]
        op = os.environ["DARK_FACTORY_REVIEW_OPERATION_ID"]
        verdict = "REQUEST_CHANGES" if state["case"] in ("base", "restart", "lost-response") and \
            pr == state["source_pr"].get("7") and not state["blocked_once"] else "ALLOW"
        arguments = {"repository": REPO, "operation_id": op, "pull_number": pr,
                     "head_sha": head, "event": verdict, "body": "Fix the first draft" if verdict == "REQUEST_CHANGES" else "Correction verified"}
        corrects = os.environ.get("DARK_FACTORY_REVIEW_CORRECTS_OPERATION_ID")
        if corrects:
            arguments["corrects_review_operation_id"] = corrects
        transaction(lambda current: app(current, "submit_pull_request_review", arguments))
        if verdict == "REQUEST_CHANGES":
            transaction(lambda current: current.__setitem__("blocked_once", True))
        output.write_text("VERDICT: " + verdict + "\n")
        return
    factoryctl = os.environ["DARK_FACTORY_FACTORYCTL"]
    def call(*argv):
        raw = subprocess.check_output([factoryctl, *argv], text=True)
        return json.loads(raw) if raw.lstrip().startswith(("{", "[")) else {}
    task = call("attempt", "task")
    body = task["task"]
    if task.get("change_id"):
        # The daemon owns this Change worktree. This deterministic provider
        # edits only its admitted worktree and reports the normal attempt API.
        cwd = os.getcwd()
        origin = int(re.search(r"FACTORY_SOURCE fixture/legacy#(\d+)", body)[1])
        filename = f"fixture-{origin}.txt"
        Path(cwd, filename).write_text("corrected\n" if task["work_revision"] > 1 else "draft\n")
        git("add", filename, cwd=cwd)
        git("-c", "user.name=fixture-worker", "-c", "user.email=worker@invalid", "commit", "-qm", "fixture change", cwd=cwd)
        transaction(lambda current: current.setdefault("worker_attempts", {}).setdefault(str(origin), []).append(
            {"task_id": task["task_id"], "work_revision": task["work_revision"], "head": git("rev-parse", "HEAD", cwd=cwd)}))
        call("attempt", "succeed", "--result", "Implemented fixture change")
        return
    if "Resume publication review" in body:
        call("attempt", "succeed", "--result", "Host review follow-up observed")
        return
    # An overseer makes the normal provider choices: delegate, then publish
    # a retained Change only after the daemon has settled its worker attempt.
    state = json.loads(STATE.read_text())
    match = re.search(r"FACTORY_SOURCE fixture/legacy#(\d+)", body)
    issue = int(match[1]) if match else None
    if issue is None and ("Operator origin fixture" in body or "Create an App-owned tracking issue" in body):
        issue = 8
    if issue == 8 and "8" not in state["issues"]:
        operation_id = str(uuid.uuid5(uuid.NAMESPACE_URL, "fixture:operator:tracking"))
        transaction(lambda current: app(current, "create_issue", {"repository": REPO,
            "operation_id": operation_id, "title": "Operator origin fixture",
            "body": "Track the accepted operator task"}))
        state = json.loads(STATE.read_text())
    key = str(issue) if issue is not None else ""
    worker = state["workers"].get(key)
    if issue is not None and not worker:
        worker = hashlib.sha256(f"fixture-worker:{issue}".encode()).hexdigest()[:32]
        incarnation = hashlib.sha256(f"fixture-incarnation:{issue}".encode()).hexdigest()[:32]
        call("overseer", "task", "add", "--agent", state["worker_id"], "--title", f"Build source #{issue}",
             "--body", f"FACTORY_SOURCE {REPO}#{issue}: implement fixture change and checks",
             "--task-id", worker, "--incarnation-id", incarnation)
        transaction(lambda current: current["workers"].__setitem__(key, worker))
        call("attempt", "succeed", "--result", f"Delegated {worker}")
        return
    if state["case"] == "restart" and not state.get("restart_retry_issued") and state["workers"].get("7"):
        # OVERSEER.md permits one same-task retry only for a proven refusal
        # before execution. This is an ordinary overseer provider decision,
        # after reading product task and Change receipts; the driver does not
        # call task update or any controller pass.
        worker_id = state["workers"]["7"]
        snapshot = call("overseer", "status", "--task", worker_id)
        failed = [row for row in snapshot["tasks"] if row["id"] == worker_id and row["status"] == "failed"]
        if failed:
            source = call("attempt", "source", "--task", worker_id)
            old_head = state.get("source_head", {}).get("7")
            old_pr = state["prs"].get(str(state.get("source_pr", {}).get("7")))
            attempts = state["worker_attempts"].get("7", [])
            safe = len(failed) == 1 and "checkout reader connection: context canceled" in failed[0]["result"] and \
                source.get("task_id") == worker_id and source.get("task_work_revision") == 2 and \
                source.get("change_id") == state.get("source_change", {}).get("7") and \
                source.get("head_commit") == old_head and source.get("dirty") is False and old_pr is not None and \
                source.get("branch") == old_pr["branch"] and old_pr["head"] == old_head and \
                len(attempts) == 1 and attempts[0]["work_revision"] == 1 and attempts[0]["head"] == old_head
            if safe:
                call("overseer", "task", "update", "--task", worker_id,
                     "--revision", str(failed[0]["revision"]), "--retry")
                transaction(lambda current: current.__setitem__("restart_retry_issued", True))
                call("attempt", "succeed", "--result", "Retried proven pre-execution failure on original worker")
                return
    # A standing wake can mention the preceding source even after its head
    # was published. Pick a still-unpublished retained head, never replay an
    # already satisfied publication merely because it was named in context.
    candidates = ([key] if worker else []) + [candidate for candidate in state["workers"] if candidate != key]
    source = None
    for candidate in candidates:
        worker_id = state["workers"][candidate]
        try:
            retained = call("attempt", "source", "--task", worker_id)
        except subprocess.CalledProcessError:
            continue
        if retained.get("head_commit") and retained["head_commit"] != state.get("source_head", {}).get(candidate):
            issue, key, worker, source = int(candidate), candidate, worker_id, retained
            break
    if source is None:
        call("attempt", "succeed", "--result", "No unpublished retained Change")
        return
    branch = "factory/" + source["change_id"][:12]
    head = source["head_commit"]
    transaction(lambda current: current.setdefault("source_change", {}).__setitem__(key, source["change_id"]))
    transaction(lambda current: current.setdefault("git_dirs", {}).__setitem__(branch, source["git_directory"]))
    filename = f"fixture-{issue}.txt"
    content = Path(source["source_path"], filename).read_bytes()
    op = str(uuid.uuid5(uuid.NAMESPACE_URL, f"fixture:{issue}:{head}:publish"))
    transaction(lambda current: app(current, "publish_commit", {"repository": REPO, "operation_id": op,
        "branch": branch, "expected_head_sha": current["published"].get(branch, current["main"]),
        "message": "Fixture publication",
        "changes": [{"path": filename, "content_base64": base64.b64encode(content).decode()}]}))
    pr = state["source_pr"].get(key)
    body = f"Fixture source #{issue} exact head {head}"
    if not pr:
        op = str(uuid.uuid5(uuid.NAMESPACE_URL, f"fixture:{issue}:pr"))
        result = transaction(lambda current: app(current, "create_pull_request", {"repository": REPO,
            "operation_id": op, "issue_number": issue, "head": branch, "head_sha": current["published"][branch],
            "base": "main", "base_sha": current["main"], "title": f"Fixture #{issue}", "body": body,
            "draft": False}))
        if state["case"] == "lost-response" and issue == 7:
            # The external create committed, but its result was lost. The
            # owner provider discards it and observes the same UUID once.
            transaction(lambda current: current.__setitem__("lost_create_operation", op))
            result = transaction(lambda current: app(current, "observe_operation", {"operation_id": op}))
            if result.get("state") != "completed" or result.get("kind") != "create_pull_request" or \
                    result.get("result", {}).get("head_sha") != head:
                raise ValueError("lost PR create response did not reconcile by the same UUID")
            result = result["result"]
        transaction(lambda current: current["source_pr"].__setitem__(key, result["number"]))
    else:
        op = str(uuid.uuid5(uuid.NAMESPACE_URL, f"fixture:{issue}:{head}:body"))
        transaction(lambda current: app(current, "update_pull_request_body", {"repository": REPO,
            "operation_id": op, "pull_number": pr, "body": body + f"\n\nRefs #{issue}"}))
    transaction(lambda current: current.setdefault("source_head", {}).__setitem__(key, head))
    call("attempt", "succeed", "--result", "Published retained Change through Maintainer App")


def main():
    name = os.environ.get("DARK_FACTORY_FIXTURE_TOOL", Path(sys.argv[0]).name)
    if name == "codex":
        provider()
    elif name == "gh":
        gh()
    elif name == "dark-factory-maintainer-mcp-bridge":
        bridge()
    elif name == "git":
        args = sys.argv[1:]
        if "fetch" in args and "origin" in args:
            args = [str(ROOT / "github.git") if arg == "origin" else arg for arg in args]
        args = [str(ROOT / "github.git") if arg.startswith("file://") and arg.endswith("/fixture/legacy") else arg for arg in args]
        os.execv(GIT, [GIT, *args])
    elif name == "fixture-deploy":
        transaction(lambda state: state.__setitem__("live", sys.argv[-1]))
    elif name == "fixture-verify":
        state = json.loads(STATE.read_text())
        response({"sha": state["live"], "healthy": True})
    else:
        raise ValueError(name)


if __name__ == "__main__":
    try:
        main()
    except Exception:
        if os.environ.get("DARK_FACTORY_FIXTURE_TOOL") == "codex":
            with (ROOT / "provider-errors.log").open("a") as stream:
                traceback.print_exc(file=stream)
        raise
