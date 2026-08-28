# AttemptResult composition and finite-recovery spec

Working spec for the daemon composition phase on `attempt-result-integration`
(kernel checkpoint ALLOW `026350a7` merged with runner checkpoint ALLOW
`c102872d`). Source of truth for this phase until the composition is built;
retire or mark residual sections when the work lands. Derived from the
corrected AttemptResult contract, the supervisor ordering (`StartBlocked` →
Store `ActivateRunner` → `child.Activate()` marker → exec release), and the
runner publish/authenticate protocol.

## Ground rules

- The Store cannot see markers. Marker/artifact/lease residue selects the
  recovery cell; the kernel transaction is chosen by Store footprint plus
  caller-proved evidence.
- Absence is only ever a positive exact-identity proof (pid+pgid+birth
  digest) plus runtime-lifetime-lease availability. A numeric PID is never
  authority.
- Lease availability semantics: flock acquirable ⇔ no live holder in the
  attempt tree. To verify in composition: the exact fd-inheritance chain
  that guarantees the provider (not just the outer runner) holds the lease
  OFD; otherwise lease availability proves less than this matrix assumes.
  (Verification result: recorded below once measured.)
- Failure details must state only what was observed (declarations-vs-
  observations rule).
- A present-but-non-authenticating artifact is evidence of a torn publish
  (O_EXCL at final name, no rename step). Never unlink it on a guess; see
  cell C3.

## Invariants that make cells impossible (fail closed if observed)

- Outer marker with Store runner=`starting`: impossible (marker is created
  only after `ActivateRunner` commits). Treat as corrupt/hostile;
  unresolved + alert.
- Any authenticated artifact without the outer marker: impossible (writer
  is the outer target, which execs only after the durable marker). Reject.
- `inner_unregistered_converged` artifact with inner marker present:
  rejected by writer, reader, and revalidation (pinned by
  TestAttemptResultMarkerMatrixIsClosed).
- Census legality constraints in `inspectRecoveredRuntimeCensus` stay
  as-is; the census grammar must be EXTENDED to admit
  `attempt-result.json` (today `validRecoveredRuntimeFile` returns
  invalid-contract for it — a blocking gap for this phase).

## Recovery cells (run non-terminal, daemon restarted)

Key: R=runner resource state, P=provider pair state, O/I=outer/inner
marker, A=artifact, L=lease available.

- **A. R=starting, P=declared, no O, no I, no A, L** →
  `RecordUnregisteredRunnerConverged`. Covers cuts from BeginRunnerStart
  through pre-ActivateRunner death, including a blocked gate child that
  died un-exec'd.
- **B. R=active(bound), P=declared, no O, no A, L, positive runner
  absence** → `RecordRecoveredPreExecRunnerAbsence`. Cut: ActivateRunner
  committed, daemon died before marker creation; the orphaned blocked
  child exits when the activation pipe closes.
- **C1. R=active, P=declared, O present, I absent, no A, L, positive
  absence** → same edge as B (the Store footprint is identical; markers
  are invisible to the Store).
- **C2. As C1 but A present and authenticates (necessarily
  inner_unregistered_converged)** → `ConsumeAttemptResult` (admitted-phase
  unregistered arm) → `RecordRecoveredRunnerAbsence` →
  `CloseTerminalAfterRunner` → `AuthorizeAttemptResultRemoval` + unlink →
  runtime release → terminal commit.
- **C3. A present but does NOT authenticate** → torn publish or tampering.
  Do not consume, do not unlink, do not treat as absence-of-result for a
  happy edge. Default fail-closed: unresolved-with-reason, retain the
  file.
- **D. R=active, P=active (identity bound), no A, L, positive absence of
  BOTH identities** → no trusted result exists; phase may be admitted or
  running. Failure proposal to finalizing (`FailRun`), provider pair
  `MarkProviderResourcesUnresolved` (release of a bound pair requires
  ProviderExit, which does not exist here), runner via
  `RecordRecoveredRunnerAbsence`, session via the recovered close edge.
- **E. A present and authenticates as inner_converged** (P declared or
  active, phase admitted/running/finalizing) → `ConsumeAttemptResult`
  (releases the provider pair in every arm) → `RecordRecoveredRunnerAbsence`
  (a restarted daemon cannot Wait a non-child) → `CloseTerminalAfterRunner`
  → `AuthorizeAttemptResultRemoval` + unlink → runtime release → terminal
  commit.
- **F. L NOT available (live holder)** → the attempt tree is alive; the
  daemon must not conclude anything. Bounded observation: watch for
  artifact appearance/lease release; never signal on recovered numeric
  identity; the notice socket is gone, so recovery consumes by polling
  authentication of the spool. Finite = bounded poll interval + no
  invented absence, not a timeout that fabricates failure.

**Sweep ordering rule (binding):** always attempt result authentication
BEFORE any absence edge. Firing `RecordRecoveredPreExecRunnerAbsence`
while a result artifact exists finalizes FailureActivation and then
`AuthorizeAttemptResultRemoval` refuses forever (orphan file, no false
exit) — fail-closed but stuck; the ordering rule prevents entering that
state.

## Composition invariants (from the checkpoint reviews)

1. **O1 (binding):** runner `AuthenticateAttemptResult` does NOT verify
   the proof — a forged artifact with a wrong proof authenticates and
   merely reports a different `ProofDigest()`. The daemon must make the
   comparison of `record.ProofDigest()` against the kernel run's stored
   result-proof digest impossible to skip: the only constructor turning a
   runner record into the kernel `AttemptResult` binds the digest from the
   record, and the Store performs the equality itself. Causal test: a
   forged-proof artifact is refused by the composed flow.
2. **O3:** `inner_converged` durable evidence carries only code/signal;
   abort context is derived from protocol state, not the artifact.
3. **O4:** a publish that fails after the O_EXCL create can leave residue
   removable only by whole-directory teardown; the census-flip case leaves
   a FULLY VALID artifact behind. Cell C3 accounts for both.
4. **O7 (threat model, document not fix):** the artifact carries the raw
   proof secret in a same-uid 0600 file; a surviving same-uid descendant
   can read it post-publication and, in the notice-less recovery path,
   unlink+forge a differently-shaped authenticating result. The proof
   defends the artifact channel, not the same-uid boundary; recovery must
   not claim otherwise.

## Migration debt closed by this phase

- internal/daemon compile failures (removed AttemptEvent.Terminal /
  AcknowledgeTerminal / AttemptSpec.TerminalName) and ~63 test failures
  (fixtures using the removed generic runner activation and
  ObserveRunnerExit) migrate to the exact edges: BeginRunnerStart →
  ActivateRunner → ActivateProviderResources; failures via FailRun /
  FailRunWithRuntimeAbsent; runner exits via RecordLiveRunnerExitAndRelease
  / RecordRecoveredRunnerAbsence; the unregistered/pre-exec edges where
  the old paths conflated them.
- After migration, per review conditions: delete the ObserveRunnerExit
  stub and the dead runner arm of observeProcessExit, the now-unproducible
  FailureRunnerExit code, and the legacy terminal-close methods including
  resultDerivedTerminalClose (same-millisecond fingerprint wedge).
- Checkpoint ALLOW conditions resolved here: `go build ./...` restored;
  full `go test ./...` green.
