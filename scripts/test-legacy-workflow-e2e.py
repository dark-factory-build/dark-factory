#!/usr/bin/env python3
"""Two real factory admissions through the installed legacy autonomy schedule.

Run from go-service-e2e.sh with its built binaries. Only setup and assertions
belong to this driver. launchd, factoryd, and the admitted provider own every
post-admission transition; legacy-workflow-fake.py supplies external effects.
"""
import hashlib
from contextlib import closing
import argparse
import json
import os
from pathlib import Path
import plistlib
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import time

HERE = Path(__file__).resolve().parent
GIT = Path("/Library/Developer/CommandLineTools/usr/bin/git")
ROOT = Path(tempfile.mkdtemp(prefix="df-legacy-e2e-", dir="/private/tmp"))
REPO = "fixture/legacy"


def run(argv, *, env=None, cwd=None, timeout=30, stdin=None):
    process = subprocess.run([str(x) for x in argv], env=env, cwd=cwd, input=stdin,
                             capture_output=True, text=True, timeout=timeout)
    if process.returncode:
        raise RuntimeError(f"{argv[0]} {argv[1:4]}: exit {process.returncode}: {process.stderr[-2000:]}")
    return process.stdout


def git(*args, cwd=None):
    return run([GIT, *args], cwd=cwd).strip()


def fixture_repository(case):
    repo, remote = ROOT / "repo", ROOT / "github.git"
    repo.mkdir()
    git("init", "--quiet", "--initial-branch=main", cwd=repo)
    (repo / "scripts").mkdir()
    gate = "#!/bin/sh\nset -eu\ntest -s fixture-7.txt -o -s fixture-8.txt\n"
    if case == "ci":
        gate += "if [ -f fixture-7.txt ] && grep -qx draft fixture-7.txt; then exit 1; fi\n"
    (repo / "scripts/local-ci.sh").write_text(gate)
    (repo / "scripts/local-ci.sh").chmod(0o755)
    (repo / "AGENTS.md").write_text("Implement the accepted fixture change and check it.\n")
    git("add", "-A", cwd=repo)
    git("-c", "user.name=fixture", "-c", "user.email=fixture@invalid", "commit", "-qm", "seed", cwd=repo)
    seed = git("rev-parse", "HEAD", cwd=repo)
    git("init", "--quiet", "--bare", remote)
    git("remote", "add", "origin", str(remote), cwd=repo)
    git("push", "-q", "origin", "main", cwd=repo)
    mirror = ROOT / "mirrors/fixture/legacy"
    mirror.parent.mkdir(parents=True)
    git("clone", "-q", "--bare", remote, mirror)
    git("--git-dir", mirror, "remote", "set-url", "origin", "https://github.com/fixture/legacy")
    return repo, seed


def tools(bin_dir):
    directory = ROOT / "tools"
    directory.mkdir()
    script = HERE / "legacy-workflow-fake.py"
    for name in ("gh", "git", "dark-factory-maintainer-mcp-bridge",
                 "fixture-deploy", "fixture-verify"):
        path = directory / name
        shutil.copy2(script, path)
        path.chmod(0o700)
    shutil.copy2(script, directory / script.name)
    run(["go", "build", "-o", directory / "codex", HERE / "legacy-workflow-provider.go"], cwd=HERE.parent)
    (directory / "codex").chmod(0o700)
    # Provider and controller invocations both resolve the same fake tools.
    return directory, str(directory) + ":" + str(bin_dir) + ":/usr/bin:/bin:/usr/sbin:/sbin"


def controller_scripts():
    destination = ROOT / "controller/scripts"
    destination.mkdir(parents=True)
    for source in HERE.iterdir():
        if source.name.startswith("factory-") and source.suffix == ".py" or source.name in (
            "cold-review.sh", "go-gate-environment.sh", "verify-adversarial-review.sh", "supervision.md"):
            shutil.copy2(source, destination / source.name)
    return destination / "factory-autonomy.py"


def ctl(binary, home, *args):
    env = dict(os.environ, DARK_FACTORY_SOCKET=str(home / "runtimes/factory.sock"),
               DARK_FACTORY_OPERATOR_TOKEN_FILE=str(home / "operator.token"))
    return json.loads(run([binary, *args], env=env))


def identity(value):
    identifier = value.get("id")
    if not isinstance(identifier, str) or len(identifier) != 32:
        raise AssertionError(f"expected factory identity, got {value}")
    return identifier


def diagnostics(home, state_path, journal):
    details = {"external": json.loads(state_path.read_text()) if state_path.exists() else None}
    for name, path in (("controller", Path(str(journal) + ".autonomy.json")),
                       ("release_controller", Path(str(journal) + ".release-autonomy.json")),
                       ("intake", journal), ("review", Path(str(journal) + ".reviews.json")),
                       ("release", ROOT / "release-journal.json")):
        if path.exists():
            details[name] = path.read_text()[-4000:]
    for name in ("controller.service.log", "controller.release.service.log"):
        path = ROOT / name
        if path.exists():
            details[name] = path.read_text()[-4000:]
    return json.dumps(details, sort_keys=True)[-16000:]


def assert_completed(state, receipt, home, operator, case, review_journal, restarted):
    source_pr = state["source_pr"]
    if set(source_pr) != {"7", "8"} or len(set(source_pr.values())) != 2:
        raise AssertionError("operator and issue origins did not publish distinct PRs")
    if state.get("create_count") != {"7": 1, "8": 1}:
        raise AssertionError("a source created more than one pull request")
    if case == "lost-response" and (state.get("lost_create_observations") != 1 or
                                    state.get("operations", {}).get(state.get("lost_create_operation"), {}).get("kind") != "create_pull_request"):
        raise AssertionError("lost create response was not reconciled by one same-UUID operation observation")
    blocked = []
    for issue, number in source_pr.items():
        pr = state["prs"][str(number)]
        if pr["state"] != "closed" or pr["head"] != state["source_head"][issue]:
            raise AssertionError(f"origin #{issue} did not merge its published exact head")
        reviews = pr["reviews"]
        allowed = [review for review in reviews if f"Dark-Factory-Review: allow {pr['head']}" in review["body"]]
        if not allowed or allowed[-1]["commit_id"] != pr["head"]:
            raise AssertionError(f"origin #{issue} has no independent exact-head ALLOW")
        if any("Dark-Factory-Review: block " in review["body"] for review in reviews):
            blocked.append(issue)
    if blocked != (["7"] if case in ("base", "restart", "lost-response") else []):
        raise AssertionError("unexpected independent review verdicts")
    issue = "7"
    attempts = state["worker_attempts"].get(issue, [])
    if len(attempts) < 2 or len({item["task_id"] for item in attempts}) != 1 or \
            attempts[-1]["work_revision"] <= attempts[0]["work_revision"] or \
            attempts[-1]["head"] == attempts[0]["head"]:
        raise AssertionError("blocking findings did not return to the original worker and change its head")
    reviews = state["prs"][str(source_pr[issue])]["reviews"]
    if case in ("base", "restart", "lost-response") and not any(
            "Dark-Factory-Review: block " + attempts[0]["head"] in review["body"] for review in reviews):
        raise AssertionError("blocked review did not name the original exact head")
    operations = json.loads(review_journal.read_text())["pulls"]
    first = operations.get(str(source_pr[issue]) + ":" + attempts[0]["head"], {})
    if case == "ci" and not (first.get("gate_state") == "failed" and first.get("gate_failure_sent_back")
                               and not first.get("review_attempted")):
        raise AssertionError("failed CI did not send the original exact head back before review")
    if case == "conflict" and not first.get("merge_conflict_sent_back"):
        raise AssertionError("reported merge conflict did not send the original exact head back")
    if case in ("ci", "conflict"):
        stale = [review for review in reviews if review["commit_id"] == attempts[0]["head"]]
        if len(stale) != 1 or stale[0]["user"]["id"] != 202 or \
                not any(review["commit_id"] == state["source_head"][issue] and review["user"]["id"] == 101
                        for review in reviews) or \
                any(value.get("kind") == "enqueue_pull_request" and
                    value.get("result", {}).get("head_sha") == attempts[0]["head"]
                    for value in state["operations"].values()):
            raise AssertionError("stale external ALLOW authorized or obscured the corrected exact head")
    if case == "restart" and not restarted:
        raise AssertionError("disposable daemon was not restarted after the blocking review")
    verified = [value for value in receipt.get("releases", {}).values() if value.get("state") == "verified"]
    delivered = {source.get("pr") for value in verified for source in value.get("delivery_sources", [])}
    delivered.update(value.get("pr") for value in verified)
    if not set(source_pr.values()) <= delivered or state["live"] != state["main"]:
        raise AssertionError("both origin PRs lack configured verified delivery")
    if receipt.get("live_tip", {}).get("sha") != state["main"]:
        raise AssertionError("release journal does not identify the final live revision")
    for issue in (7, 8):
        content = git("--git-dir", ROOT / "github.git", "show", state["main"] + f":fixture-{issue}.txt")
        if content != ("corrected" if issue == 7 else "draft"):
            raise AssertionError(f"merged tree lost source #{issue}")
    database = (home / "factory.sqlite3").as_uri() + "?mode=ro"
    with closing(sqlite3.connect(database, uri=True)) as connection:
        rows = connection.execute("""SELECT p.pull_number, lower(hex(p.task_id)), lower(hex(c.task_id)),
                                          lower(hex(c.head_commit)), t.title
                                   FROM publication_tasks p JOIN changes c ON c.id = p.change_id
                                   JOIN tasks t ON t.id = c.task_id
                                   WHERE p.repository = ?""", (REPO,)).fetchall()
        if len(rows) != 2:
            raise AssertionError("publication lacks two durable Change associations")
        for issue in (7, 8):
            match = [row for row in rows if row[0] == source_pr[str(issue)]]
            worker = state["worker_attempts"][str(issue)][-1]["task_id"]
            if len(match) != 1 or match[0][1:4] != (worker, worker, state["source_head"][str(issue)]) \
                    or match[0][4] != f"Build source #{issue}":
                raise AssertionError(f"PR for source #{issue} is not bound to its original worker Change")
        for title, worker in (("Operator origin fixture", state["worker_attempts"]["8"][0]["task_id"]),
                              ("Supervise GitHub #7: Issue origin fixture", state["worker_attempts"]["7"][0]["task_id"])):
            source = connection.execute("SELECT lower(hex(id)), result FROM tasks WHERE title = ?", (title,)).fetchall()
            if len(source) != 1 or source[0][1] != "Delegated " + worker or \
                    (title == "Operator origin fixture" and source[0][0] != operator):
                raise AssertionError(f"original source task did not durably delegate {worker}")
        if case == "restart" and state.get("restart_retry_issued"):
            worker = bytes.fromhex(state["worker_attempts"]["7"][0]["task_id"])
            # ActivateRun sets running_at before the held provider is released,
            # so an unset running_at proves the agent never executed even when
            # the restart killed an already spawned, still held provider.
            preactivation = connection.execute(
                """SELECT r.running_at_ms, r.terminal_kind
                   FROM runs r WHERE r.task_id = ? AND r.admitted_task_work_revision = 2""",
                (worker,)).fetchall()
            if len(preactivation) != 1 or preactivation[0] != (None, "failed"):
                raise AssertionError("restart retry did not follow a settled pre-activation failure")
    if "8" not in state["issues"] or "<!-- dark-factory-operation:" not in state["issues"]["8"]["body"]:
        raise AssertionError("operator origin has no App-owned tracking issue")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", choices=("base", "ci", "conflict", "restart", "lost-response"), default="base")
    case = parser.parse_args().case
    bin_dir = Path(os.environ["DARK_FACTORY_E2E_FACTORYD"]).parent
    factoryd, factoryctl = bin_dir / "factoryd", bin_dir / "factoryctl"
    home = ROOT / "home"
    repository, seed = fixture_repository(case)
    tool_dir, path = tools(bin_dir)
    controller = controller_scripts()
    state_path = ROOT / "external.json"
    state_path.write_text(json.dumps({"case": case, "seed": seed, "main": seed, "live": seed,
        "issues": {"7": {"number": 7, "title": "Issue origin fixture", "body": "Implement fixture result",
                  "state": "OPEN", "author": {"login": "fixture-owner"}, "labels": [{"name": "factory:ready"}],
                  "updatedAt": "2026-09-23T00:00:00Z", "url": f"https://github.com/{REPO}/issues/7"}},
        "operations": {}, "published": {}, "prs": {}, "workers": {}, "source_pr": {},
        "blocked_once": False, "worker_id": "", "worker_attempts": {}}))
    run([factoryctl, "init", "--home", home])
    # The installed legacy production recorder resolves the sibling
    # factoryctl from the service layout, even when this fixture starts the
    # matching daemon directly to avoid installing a second intake scheduler.
    installed_bin = Path(str(home) + ".service/bin/current")
    installed_bin.mkdir(parents=True)
    (installed_bin / "factoryctl").symlink_to(factoryctl)
    daemon_env = dict(os.environ, PATH=path)
    log = (ROOT / "factoryd.log").open("w")
    daemon_command = [str(factoryd), "--home", str(home), "--git", str(GIT),
                      "--tool-path", path, "--development-browser-address", "127.0.0.1:0"]
    daemon = subprocess.Popen(daemon_command, env=daemon_env, stdout=log, stderr=subprocess.STDOUT)
    labels = []
    try:
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            try:
                ctl(factoryctl, home, "status")
                break
            except Exception:
                time.sleep(.1)
        else:
            raise AssertionError("factoryd did not become ready: " + (ROOT / "factoryd.log").read_text()[-2000:])
        project = identity(ctl(factoryctl, home, "project", "create", "--name", "fixture", "--root", repository))
        worker = identity(ctl(factoryctl, home, "agent", "create", "--project", project,
                              "--name", "worker", "--role", "worker", "--provider", "codex", "--tool-budget", "1000"))
        overseer = identity(ctl(factoryctl, home, "agent", "create", "--project", project,
                                "--name", "overseer", "--role", "orchestrator", "--provider", "codex", "--tool-budget", "1000"))
        ctl(factoryctl, home, "agent", "idle-policy", "--agent", overseer, "--revision", "1",
            "--policy", "standing_instruction", "--after-seconds", "5",
            "--instruction", "Reconcile settled workers and continue their source publications through the Maintainer App.",
            "--run-budget", "20")
        state = json.loads(state_path.read_text())
        state["worker_id"] = worker
        state_path.write_text(json.dumps(state))
        journal = ROOT / "controller.json"
        release = ROOT / "release.json"
        release.write_text(json.dumps({"repository": REPO, "base": "main", "journal": str(ROOT / "release-journal.json"),
            "destination": "runtime:fixture",
            "deploy_argv": [str(tool_dir / "fixture-deploy")], "verify_argv": [str(tool_dir / "fixture-verify")],
            "review_verifier": [str(controller.parent / "verify-adversarial-review.sh")], "command_timeout": 15}))
        config = ROOT / "intake.json"
        config.write_text(json.dumps({"factory_home": str(home), "journal": str(journal), "repository": REPO,
            "project_id": project, "overseer_agent_id": overseer, "label": "factory:ready",
            "allowed_authors": ["fixture-owner"], "review_mirror_root": str(ROOT / "mirrors"),
            "poll_seconds": 5, "release_configs": [str(release)], "review_provider": "codex"}))
        # Both launchd jobs are generated by the installed controller. They
        # run ordinary passes after admission; this driver never calls --once.
        for release_only in (False, True):
            arguments = [sys.executable, str(controller), str(config), "--plist"]
            if release_only:
                arguments.append("--release-only")
            plist = plistlib.loads(subprocess.check_output(arguments, env=daemon_env))
            plist_path = ROOT / ("release.plist" if release_only else "intake.plist")
            plist_path.write_bytes(plistlib.dumps(plist))
            run(["/bin/launchctl", "bootstrap", f"gui/{os.geteuid()}", plist_path])
            labels.append(plist["Label"])
        # This is setup, before measurement begins. The operator task and
        # intake issue are separate origins; the daemon admits both.
        operator = identity(ctl(factoryctl, home, "task", "add", "--project", project,
                                "--agent", overseer, "--title", "Operator origin fixture",
                                "--body", "Create an App-owned tracking issue; delegate, publish, review, merge, and deliver."))
        ctl(factoryctl, home, "dispatch", "on")
        deadline = time.monotonic() + 300
        restarted = False
        while time.monotonic() < deadline:
            state = json.loads(state_path.read_text())
            if case == "restart" and state["blocked_once"] and not restarted:
                # Fault injection touches only this disposable daemon. The
                # scheduled product controllers still own all recovery work.
                daemon.terminate()
                daemon.wait(timeout=10)
                daemon = subprocess.Popen(daemon_command, env=daemon_env, stdout=log, stderr=subprocess.STDOUT)
                ready_until = time.monotonic() + 20
                while time.monotonic() < ready_until:
                    try:
                        ctl(factoryctl, home, "status")
                        break
                    except Exception:
                        time.sleep(.1)
                else:
                    raise AssertionError("factoryd did not recover after restart")
                restarted = True
            receipt = json.loads((ROOT / "release-journal.json").read_text()) if (ROOT / "release-journal.json").exists() else {}
            if len(state["prs"]) == 2 and state["live"] == state["main"] and all(pr["state"] == "closed" for pr in state["prs"].values()) \
                    and all(receipt.get("releases", {}).get(str(pr["number"]), {}).get("state") == "verified"
                            for pr in state["prs"].values()) and receipt.get("live_tip", {}).get("sha") == state["main"]:
                assert_completed(state, receipt, home, operator, case, Path(str(journal) + ".reviews.json"), restarted)
                print(json.dumps({"case": case, "operator_task": operator, "prs": state["prs"], "release": receipt}, sort_keys=True))
                return
            status = ctl(factoryctl, home, "status")
            failed_sources = [task for task in status.get("tasks", []) if task["status"] == "failed" and
                              task["title"] in ("Operator origin fixture", "Supervise GitHub #7: Issue origin fixture", "Standing instruction")]
            if failed_sources and len(state["source_pr"]) < 2:
                raise AssertionError("admitted source task failed: " + diagnostics(home, state_path, journal))
            for name in ("controller.service.log", "controller.release.service.log"):
                log_text = (ROOT / name).read_text() if (ROOT / name).exists() else ""
                if "Traceback (most recent call last):" in log_text:
                    raise AssertionError("controller crashed: " + diagnostics(home, state_path, journal))
            time.sleep(.5)
        raise AssertionError("legacy workflow timed out: " + diagnostics(home, state_path, journal))
    finally:
        for label in labels:
            subprocess.run(["/bin/launchctl", "bootout", f"gui/{os.geteuid()}/{label}"], capture_output=True)
        daemon.terminate()
        try:
            daemon.wait(timeout=5)
        except subprocess.TimeoutExpired:
            daemon.kill()
            daemon.wait()
        log.close()
        if os.environ.get("DARK_FACTORY_KEEP_E2E") != "1":
            shutil.rmtree(ROOT, ignore_errors=True)


if __name__ == "__main__":
    main()
