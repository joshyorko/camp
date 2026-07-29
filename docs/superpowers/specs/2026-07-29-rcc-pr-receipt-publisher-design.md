# RCC PR Receipt Publisher Design

## Objective

Give humans and agents one repository-owned RCC task for creating or updating a
Camp pull request without bypassing the mandatory evidence receipt. Keep CI as
the final enforcement boundary, but move the same validation ahead of the
GitHub mutation so an incomplete body is never published.

The intended command is:

```sh
rcc run -r developer/toolkit.yaml --dev -t pr
```

`AGENTS.md` will require this task for Camp pull-request creation and body
updates. Direct `gh pr create` and `gh pr edit` remain available for repository
administration, but are not valid agent publication paths.

## Repository-owned components

### Receipt model

Move the canonical receipt labels and parsing rules into one importable
repository module. Both the RCC publisher and
`developer/verify_pr_receipt.py` use that module. The pull-request template
remains the human-readable presentation of the same labels, and contract tests
fail if the template, publisher, or CI verifier diverge.

The model distinguishes:

- machine evidence: candidate digest and matching RCC gate reports;
- author evidence: behavior/safety prose, real-tool interpretation,
  release-note classification, and the documentation receipt;
- publication state: explicit implemented, tested, pushed, merged, packaged,
  released, deployed, and dogfooded claims.

The publisher never invents author evidence and never treats stale gate reports
as current.

### RCC `pr` development task

Add a `pr` entry under `devTasks` in `developer/toolkit.yaml`. Its implementation
uses the current checkout, `gh`, `.github/pull_request_template.md`, and ignored
files below `build/evidence/`; it does not require another service or token
beyond the authenticated GitHub CLI.

The task:

1. Requires a clean named branch with an upstream push.
2. Resolves an existing pull request for that head branch, if one exists.
3. Uses the existing PR body as the prose source; for a new PR it uses
   `build/evidence/pr-body.md`, creating that draft from the canonical template
   and stopping before publication when the draft is absent.
4. Reads `build/evidence/candidate.json` and only consumes `*-gates.json`
   reports whose `candidateSha256` matches that candidate.
5. Updates machine-derived receipt fields without replacing unrelated prose.
6. Requires every author-derived field to contain a non-placeholder value.
7. Renders the complete candidate body to `build/evidence/pr-body.md`, validates
   it with the shared receipt model, and only then invokes `gh pr create` or
   `gh pr edit`.
8. Reads the live PR back and validates the persisted body before reporting
   success.

For a new PR, the default title is the current commit subject. An optional
`CAMP_PR_TITLE` overrides it. The base branch defaults to the repository default
branch and may be overridden with `CAMP_PR_BASE`. These are non-secret task
inputs.

## Data and preservation rules

Receipt fields are managed by exact canonical label, not by replacing whole
Markdown sections. A field appearing zero times, more than once, empty, or with
a placeholder such as `TODO`, `TBD`, or the untouched template hint blocks the
GitHub mutation.

When no matching RCC report exists, the task records that gate evidence under
`Missing or skipped gates`; it does not reuse an older report or claim a pass.
`Candidate SHA-256` comes only from a clean candidate manifest for the current
commit. A missing or mismatched manifest blocks publication and tells the
operator to run the RCC `local` task.

The author must classify release notes explicitly. The task accepts only
`docs/changelog.md` or `no-release-note: REASON`, matching CI. Documentation
receipt fields remain human/agent-authored because their meaning cannot be
derived safely from a diff.

## Failure and recovery

All validation happens before a GitHub write. On failure the task preserves the
rendered draft, prints the exact missing, stale, duplicate, or invalid fields,
and exits nonzero with the next command. It does not create a partial PR.

If GitHub creation or editing fails, rerunning is idempotent: the task resolves
the branch PR again and applies the same validated body. If post-write readback
differs, the task fails without claiming publication and retains the local
candidate body for inspection.

Existing prose and unknown sections are byte-preserved except for normalized
canonical receipt lines. The task refuses multiple open PRs for the same head
branch rather than guessing.

## Tests

Unit tests cover receipt parsing, placeholder rejection, duplicate labels,
preservation of unrelated Markdown, release-note validation, and deterministic
rendering. Factory tests cover candidate/report digest matching and stale
evidence rejection.

Subprocess tests use a fake `gh` executable to prove:

- new-PR creation stops before mutation when the draft is incomplete;
- existing PR repair updates only canonical receipt fields;
- create/edit is followed by live readback validation;
- GitHub failures remain nonzero and rerunnable;
- no secret environment values are written to the body or evidence files.

Release-pipeline contract tests keep `AGENTS.md`, the RCC task, PR template, and
CI verifier synchronized. Focused tests, full RCC source gates, `go vet ./...`,
`go build ./cmd/camp`, and `git diff --check` remain the implementation exit
gate.

## Documentation contract

`AGENTS.md` will name the RCC `pr` task as the required agent path and prohibit
direct PR-body mutation by agents. `docs/skills/testing-release-evidence.md`
will document the task, its evidence freshness rules, and the distinction
between a locally validated body, a persisted PR, passing CI, merge, and
release.

