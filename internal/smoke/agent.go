package smoke

// agentScript is the fake agent: no model, it does what a build session does at the end of a run.
// It writes the one file the ticket asks for, commits on the ticket branch, pushes it, pushes a
// report commit to report/<ticket> (the technique the brief gives). It does not open the pull
// request: the loop opens it once it sees the branch and the report, and an agent that opened its own
// raced that and left two pull requests for one branch.
const agentScript = `#!/bin/sh
# Fake agent for the factory smoke test. Needs SMOKE_FILE and optionally SMOKE_LOG in its
# environment. Runs inside the ticket's worktree.
set -eu
[ -z "${SMOKE_LOG:-}" ] || exec 2>>"$SMOKE_LOG"
branch=$(git rev-parse --abbrev-ref HEAD)
ticket=${branch#factory/}
ticket=${ticket%-a*}
mkdir -p "$(dirname "$SMOKE_FILE")"
printf 'smoke test file for ticket %s\n' "$ticket" > "$SMOKE_FILE"
git add -- "$SMOKE_FILE"
git -c user.name=smoke-agent -c user.email=smoke-agent@localhost -c commit.gpgsign=false commit -q -m "smoke: add $SMOKE_FILE"
git push -q origin "HEAD:refs/heads/$branch"
blob=$(printf 'Smoke run for ticket %s: added %s.\n' "$ticket" "$SMOKE_FILE" | git hash-object -w --stdin)
tree=$(printf '100644 blob %s\treport.md\n' "$blob" | git mktree)
commit=$(git -c user.name=smoke-agent -c user.email=smoke-agent@localhost -c commit.gpgsign=false commit-tree "$tree" -m report)
git push -q origin "$commit:refs/heads/report/$ticket"
`
