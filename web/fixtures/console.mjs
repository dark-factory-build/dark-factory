// The typed stand-in for console data the daemon does not serve yet. Each
// field maps to a GAPS entry in docs/internal/web-console-design.md; live
// wiring replaces fields one by one without touching the components.
const taskID = "31".repeat(16);
const queuedTaskID = "32".repeat(16);
const doneTaskID = "33".repeat(16);
const failedTaskID = "34".repeat(16);
const agentID = "21".repeat(16);
const secondAgentID = "22".repeat(16);
const thirdAgentID = "23".repeat(16);

export const fixtureConsoleExtras = {
  ticker: [
    { at: "14:18", text: "run for “Probe the flaky gate” failed" },
    { at: "14:29", text: "Builder One asks about the users table" },
    { at: "14:32", text: "“Close the resize race” merged" },
  ],
  suggestions: [
    { id: "51".repeat(16), title: "Fix the doctor exit code on a missing socket", origin: "github", detail: "issue #204 — labelled for the factory by the owner" },
    { id: "52".repeat(16), title: "P95 latency regression on state pages", origin: "telemetry", detail: "observed in production over the last 6 hours" },
  ],
  awaitingDeploy: 2,
  stages: new Map([[doneTaskID, "done"], [taskID, "building"]]),
  reviews: new Map([
    [doneTaskID, { outcome: "approved", findings: ["tried to break the resize path with a zero-width pty; the guard held"] }],
    [failedTaskID, { outcome: "blocked", findings: ["the retry loop could spin forever on a closed descriptor"] }],
  ]),
  diffs: new Map([
    [taskID, { additions: 42, deletions: 18 }],
    [doneTaskID, { additions: 156, deletions: 9 }],
  ]),
  rowTicks: new Map([[taskID, "$ go test ./internal/kernel/"]]),
  records: new Map([
    [doneTaskID, {
      asked: "Close the race between terminal resize and detach observed in the browser loop.",
      happened: "The run reproduced the race with a causal test, then serialized resize behind the attach gate. Review approved on the second pass.",
      runNumber: 2,
      review: { outcome: "approved", findings: ["tried to break the resize path with a zero-width pty; the guard held", "the first pass missed a stale-generation fence; fixed and re-checked"] },
      checks: ["all 84 web tests passed", "every process started was reaped, every resource released"],
      files: [
        { path: "internal/runner/pty_darwin.go", additions: 31, deletions: 7, patch: "@@ -140,7 +140,12 @@\n-  resize(rows, cols)\n+  gate.serialize(func() { resize(rows, cols) })" },
        { path: "internal/runner/pty_darwin_test.go", additions: 125, deletions: 2, patch: "@@ -300,2 +300,15 @@\n+func TestResizeSerializesBehindAttach(t *testing.T) { … }" },
      ],
      transcript: "run 2 · Builder Two\n> reproducing the race…\n> causal test red on the old code\n> fix applied; test green",
    }],
  ]),
  agentGlyphs: new Map([[agentID, "C"], [secondAgentID, "◆"], [thirdAgentID, "X"]]),
};
