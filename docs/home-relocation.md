# Factory-home relocation

The supported design is an explicit, stopped-home move (`factoryctl home move
--from ABS --to ABS`), rather than converting durable state to home-relative
paths.

Home-relative storage is not a safe small migration. The store contains
absolute project roots and absolute runtime resource locators, and linked Git
worktrees put absolute `gitdir` and worktree paths in the source repository's
`.git/worktrees` administration. Those values are also consumed by the
daemon, runner, Change worker, recovery, and status paths. Changing their
representation would require a schema migration plus a coordinated change to
every reader and would invalidate any digest that covers the serialized
record. A path-independent UUID does not help when the digest is over a
record containing the path.

The move operation must therefore:

1. Refuse a non-absolute, equal, existing, symlinked, or operator-unowned
   source/target; refuse unless the service is stopped and every run is
   terminal.
2. Verify the source with `doctor`, take a mode-preserving verified backup,
   and stage the destination beside the source.
3. Rewrite only path-bearing durable values from the exact old prefix to the
   exact new prefix in one SQLite transaction. Reject a value that is not an
   exact path or path descendant; never do an unrestricted byte replacement.
4. Run `git worktree repair` for every registered Change worktree and verify
   each worktree against its recorded repository identity and commit.
5. Refuse relocation while a service artifact exists. The supported service
   procedure below stops and uninstalls it first, then reinstalls it at the
   destination after publication.
6. Run `doctor` against the staged destination, publish it with an atomic
   rename, then repair external Git metadata and run doctor plus a status read
   at the destination. A post-publish failure restores the old home and the
   saved Git metadata; the verified backup remains for manual recovery.

Recovery cannot atomically replace a live service receipt: launchd stores its
label and plist outside the home, and the receipt digest is part of the
service's identity. The supported recovery procedure is therefore to prove
the canonical service is stopped, uninstall the stale receipt, quarantine the
dead canonical directory, move the recovered home into the canonical path,
and reinstall the service there. This is the narrowly bounded case where the
operator must coordinate launchd; it never leaves two discoverable homes.

The implementation includes the stopped-home command and a Darwin temporary
home proof for initialization, staging, relocation, and doctor. Full service
receipt reinstallation remains deliberately refused when a service artifact
is present: launchd's label and plist live outside the home and cannot be
atomically moved by a filesystem rename. The supported safe procedure is:

```text
factoryctl service stop --home OLD
factoryctl service status --home OLD
factoryctl service uninstall --home OLD
factoryctl home move --from OLD --to NEW
factoryctl service install --home NEW
factoryctl doctor --home NEW
factoryctl status
```

Restore any non-default install options recorded by `service status` when
installing at `NEW`. This is safer than silently changing a launch agent while
a different process may still own it.

The checked-in proof covers a disposable initialized home, refusal that leaves
the source unchanged, and a populated project with a registered linked Change
and terminal run; it runs doctor and a status-equivalent store snapshot at the
new path. Worktree-repair failure snapshots and restores the external Git
administration before rolling the home rename back, so a failed move cannot
leave links pointing at the unpublished destination.

If a process crash leaves `.move-old`, `.move-stage-*`, or
`.move-backup-*`, do not delete either home or any backup blindly. Stop and
uninstall the service, confirm no run is non-terminal, inspect the candidates
with `doctor`, and retain the newest complete verified home. Remove only the
unselected stage/backup after the selected home is healthy; if `.move-old` and
the destination both exist, use the destination only after doctor and status
pass, otherwise restore `.move-old` to the original name. Reinstall the
service only after one canonical home remains. This procedure is the supported
collision recovery boundary because launchd and external Git metadata cannot
be committed in the same filesystem rename transaction.
