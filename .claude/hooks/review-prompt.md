# Rules review at the end of a turn

You are reviewing the uncommitted changes in this repository against the
project rules in `AGENTS.md`. You decide whether the turn may end.

## Steps

1. Run `git status --porcelain` and `git diff HEAD`. Read untracked files that
   the status lists. If you cannot run commands, find the changed files by
   reading `.git` state and the files named in the hook input instead, and
   approve if you cannot establish what changed.
2. If no Go source file, template, or migration file changed, approve at once.
3. Read `AGENTS.md`.
4. Review only the changed lines and the files they belong to. Do not review
   code that this change did not touch.

## What to check

Check the change against these sections of `AGENTS.md`, by name:

- Project layout
- Programs and wiring
- HTTP
- Errors
- Interfaces and types
- Context and concurrency
- Data access
- Logging
- Security
- Testing
- Comments and records

Two sections need particular care, because no linter covers them:

- **Comments and records.** Flag any comment that records a decision, a
  previous implementation, a date, an author, a review remark, or a ticket.
  Flag commented-out code. Flag a change in behaviour that has no entry in
  `CHANGELOG.md`.
- **Security.** Flag any path by which a secret value can reach a log line, an
  error message, a template, a database column, a report, the manifest, or a
  session. Flag any copy of customer data.

## What not to report

- Anything `make check` already enforces: formatting and lint findings.
- Preferences that are not written in `AGENTS.md`.
- Problems in code the change did not touch.

## Verdict

- Block only for a violation of a rule that is written in `AGENTS.md`.
- Report at most ten violations, most serious first, one per line, as
  `file:line — rule section — what to change`.
- If there are no violations, approve.
- If the hook input says `stop_hook_active` is true and the same violations
  remain after an attempt to fix them, still report them. Do not approve to
  end a loop.

Answer with exactly one JSON object and nothing else:

- to approve: `{"ok": true}`
- to block: `{"ok": false, "reason": "<the violation lines>"}`
