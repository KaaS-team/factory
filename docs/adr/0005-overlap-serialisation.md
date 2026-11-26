# 0005. Issues that plan the same files wait for each other

## Status

accepted

## Context

Two issues that each plan a change to the same file can both build and open pull requests. The
second pull request then conflicts with the first and is built again.

## Decision

`internal/pick` holds the rule. `Refill` (`internal/pick/pick.go`) fills free slots from the ranked
ready tickets. `overlap` compares a ticket's planned `Files` with those of every running ticket and
every held lock (`Locks.Files`, `Locks.Unmerged`) using `Overlap`. A ticket that overlaps waits and
the skip reason names the holder, for example `files overlap <ticket>`. When both branches exist,
`Locks.Merge` runs a merge-tree check and the ticket waits only if the merge would conflict.

`internal/plan/dag.go` applies the same file test at plan time: `Plan.Waves` puts tickets whose
`Files` overlap in different waves.

## Consequences

- A later issue waits until the earlier one leaves flight, which happens when its pull request merges
  or closes.
- The check reads the planned files, so a plan that names too few files lets an overlap through.
- The skip reason names the first blocker. A clean merge-tree result lets overlapping files run side by side.

## Alternatives considered

- Compare the diffs after the build. Rejected: the build cost has already been paid.
- Block on any open issue with the same label. Rejected: unrelated files would wait.
