# RCC PR Receipt Publisher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add one repository-owned RCC `pr` task that validates, creates, or updates Camp pull requests without publishing an incomplete evidence receipt.

**Architecture:** Extract receipt parsing and validation into a pure Python module shared by CI and the publisher. Add a second Python module that loads only current-candidate RCC evidence, preserves PR prose, and performs validated `gh` create/edit/readback operations; expose it through `tasks.py` and `developer/toolkit.yaml`. Keep author evidence explicit, make all GitHub writes validation-last, and contract-test the RCC task, `AGENTS.md`, PR template, and CI verifier together.

**Tech Stack:** Python 3.10.15 standard library, Invoke 2.2.0, RCC `devTasks`, GitHub CLI, Go release-pipeline contract tests.

## Global Constraints

- Run on Linux in the repository RCC environment; do not add Python dependencies.
- The only public command is `rcc run -r developer/toolkit.yaml --dev -t pr`.
- Never invent author evidence or reuse a gate report whose `candidateSha256` differs from `build/evidence/candidate.json`.
- Validate the complete body before every GitHub write and validate the persisted body after the write.
- Preserve unrelated PR prose and unknown Markdown sections.
- Keep `developer/verify_pr_receipt.py` as the GitHub Actions entry point.
- Agents must use the RCC task instead of direct `gh pr create` or `gh pr edit`.
- Update `docs/skills/testing-release-evidence.md` in the implementation commit; do not add a run diary.

---

## File Structure

- Create `developer/pr_receipt.py`: canonical labels, parsing, field replacement, placeholder rejection, and release-note validation.
- Create `developer/test_pr_receipt.py`: pure receipt-model unit tests.
- Modify `developer/verify_pr_receipt.py`: decode the event and delegate body validation to `pr_receipt`.
- Modify `developer/verify_pr_receipt_test.py`: retain event-entry-point coverage and assert shared-model diagnostics.
- Create `developer/pr_publisher.py`: repository/evidence inspection, PR body preparation, `gh` create/edit/readback, and idempotent publication.
- Create `developer/test_pr_publisher.py`: evidence freshness and fake-`gh` subprocess tests.
- Modify `tasks.py`: expose the Invoke `pr` task.
- Modify `developer/toolkit.yaml`: expose the RCC `pr` development task.
- Modify `developer/test_tasks.py`: prove task delegation.
- Modify `AGENTS.md`: require the RCC publisher for agent PR mutations.
- Modify `docs/skills/testing-release-evidence.md`: document the implemented command, evidence freshness, and publication boundaries.
- Modify `releasepipeline/documentation_contract_test.go`: bind `AGENTS.md`, the toolkit task, template, publisher, and verifier.
- Modify `releasepipeline/rcc_factory_contract_test.go`: retain black-box verifier coverage against the shared model.

---

### Task 1: Shared receipt model and CI verifier

**Files:**
- Create: `developer/pr_receipt.py`
- Create: `developer/test_pr_receipt.py`
- Modify: `developer/verify_pr_receipt.py`
- Modify: `developer/verify_pr_receipt_test.py`
- Modify: `releasepipeline/rcc_factory_contract_test.go`

**Interfaces:**
- Produces: `ReceiptError(ValueError)`.
- Produces: `parse_fields(body: str) -> dict[str, str]`.
- Produces: `validate_body(body: str) -> dict[str, str]`.
- Produces: `replace_field(body: str, label: str, value: str) -> str`.
- Consumes: no repository state and no subprocesses.

- [ ] **Step 1: Write failing receipt-model unit tests**

Create `developer/test_pr_receipt.py` with focused cases:

```python
import unittest

from developer.pr_receipt import ReceiptError, replace_field, validate_body


VALID_BODY = """## Verification
- Passed gates: unit
- Failed gates: none
- Missing or skipped gates: robot not run
- Candidate SHA-256: abc123
- Real-tool evidence: not run
- Release-note classification: no-release-note: internal workflow only

## Documentation improvement
- Canonical file changed or proposed: docs/skills/testing-release-evidence.md
- Durable learning captured: PR bodies are validated before publication
- Evidence: developer/test_pr_receipt.py
- Stale or ambiguous guidance removed: template use is not automatic
- Remaining uncertainty: hosted CI not run
"""


class ReceiptModelTest(unittest.TestCase):
    def test_validate_body_returns_exact_fields(self):
        fields = validate_body(VALID_BODY)
        self.assertEqual(fields["Candidate SHA-256:"], "abc123")

    def test_validate_body_rejects_duplicate_field(self):
        body = VALID_BODY + "\n- Candidate SHA-256: replacement\n"
        with self.assertRaisesRegex(ReceiptError, "duplicate.*Candidate SHA-256"):
            validate_body(body)

    def test_validate_body_rejects_placeholder_value(self):
        body = VALID_BODY.replace("Real-tool evidence: not run", "Real-tool evidence: TODO")
        with self.assertRaisesRegex(ReceiptError, "placeholder.*Real-tool evidence"):
            validate_body(body)

    def test_replace_field_preserves_unrelated_markdown(self):
        body = replace_field(VALID_BODY, "Passed gates:", "unit, race")
        self.assertIn("## Documentation improvement", body)
        self.assertIn("- Passed gates: unit, race", body)
        self.assertEqual(body.count("Passed gates:"), 1)
```

- [ ] **Step 2: Run the new tests and confirm the import failure**

Run:

```sh
python3 -m unittest developer.test_pr_receipt -v
```

Expected: fail because `developer.pr_receipt` does not exist.

- [ ] **Step 3: Implement the pure receipt model**

Create `developer/pr_receipt.py` with the canonical field order and exact validation:

```python
import re


REQUIRED_FIELDS = (
    "Passed gates:",
    "Failed gates:",
    "Missing or skipped gates:",
    "Candidate SHA-256:",
    "Real-tool evidence:",
    "Release-note classification:",
    "Canonical file changed or proposed:",
    "Durable learning captured:",
    "Evidence:",
    "Stale or ambiguous guidance removed:",
    "Remaining uncertainty:",
)
MACHINE_FIELDS = REQUIRED_FIELDS[:4]
AUTHOR_FIELDS = REQUIRED_FIELDS[4:]
LABEL_PREFIX = re.compile(r"^\s*[-*]\s*")
PLACEHOLDERS = {"todo", "tbd", "replace me", "<replace>", "fill me"}


class ReceiptError(ValueError):
    pass


def parse_fields(body: str) -> dict[str, str]:
    found = {field: [] for field in REQUIRED_FIELDS}
    for line in body.splitlines():
        normalized = LABEL_PREFIX.sub("", line, count=1)
        for field in REQUIRED_FIELDS:
            if normalized.startswith(field):
                found[field].append(normalized[len(field):].strip())
                break
    duplicates = [field for field, values in found.items() if len(values) > 1]
    missing = [field for field, values in found.items() if not values or not values[0]]
    if duplicates:
        raise ReceiptError("duplicate pull request receipt fields: " + ", ".join(duplicates))
    if missing:
        raise ReceiptError("missing pull request receipt fields: " + ", ".join(missing))
    return {field: values[0] for field, values in found.items()}


def validate_body(body: str) -> dict[str, str]:
    fields = parse_fields(body)
    placeholders = [
        field for field, value in fields.items()
        if value.strip().lower() in PLACEHOLDERS
    ]
    if placeholders:
        raise ReceiptError("placeholder pull request receipt fields: " + ", ".join(placeholders))
    release_note = fields["Release-note classification:"]
    if release_note != "docs/changelog.md" and not (
        release_note.startswith("no-release-note:")
        and release_note.removeprefix("no-release-note:").strip()
    ):
        raise ReceiptError(
            "invalid release-note classification: use docs/changelog.md "
            "or no-release-note: REASON"
        )
    return fields


def replace_field(body: str, label: str, value: str) -> str:
    if label not in REQUIRED_FIELDS or not value.strip():
        raise ReceiptError(f"invalid receipt replacement for {label}")
    output = []
    replaced = 0
    for line in body.splitlines():
        normalized = LABEL_PREFIX.sub("", line, count=1)
        if normalized.startswith(label):
            prefix = line[: len(line) - len(normalized)]
            output.append(f"{prefix}{label} {value.strip()}")
            replaced += 1
        else:
            output.append(line)
    if replaced != 1:
        raise ReceiptError(f"expected exactly one {label} field, found {replaced}")
    suffix = "\n" if body.endswith("\n") else ""
    return "\n".join(output) + suffix
```

- [ ] **Step 4: Run the pure tests and make them pass**

Run:

```sh
python3 -m unittest developer.test_pr_receipt -v
```

Expected: all receipt-model tests pass.

- [ ] **Step 5: Refactor the CI entry point to use the shared model**

Replace the duplicated constants and parser in `developer/verify_pr_receipt.py` with:

```python
import json
import pathlib
import sys

from developer.pr_receipt import ReceiptError, validate_body


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: verify_pr_receipt.py GITHUB_EVENT_PATH", file=sys.stderr)
        return 2
    event = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
    body = (event.get("pull_request") or {}).get("body") or ""
    try:
        validate_body(body)
    except ReceiptError as error:
        print(str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
```

Update `developer/verify_pr_receipt_test.py` assertions to expect the shared
`missing pull request receipt fields` and `invalid release-note classification`
messages. Update the Go black-box test only where the exact diagnostic changed.

- [ ] **Step 6: Run focused Python and Go contract tests**

Run:

```sh
python3 -m unittest developer.test_pr_receipt developer.verify_pr_receipt_test -v
go test ./releasepipeline -run 'TestPullRequestReceipt' -count=1
```

Expected: all tests pass.

- [ ] **Step 7: Commit the shared model**

```sh
git add developer/pr_receipt.py developer/test_pr_receipt.py \
  developer/verify_pr_receipt.py developer/verify_pr_receipt_test.py \
  releasepipeline/rcc_factory_contract_test.go
git commit -m "refactor: share pull request receipt validation"
```

---

### Task 2: Current-candidate evidence aggregation and body preparation

**Files:**
- Create: `developer/pr_publisher.py`
- Create: `developer/test_pr_publisher.py`

**Interfaces:**
- Consumes: `validate_body` and `replace_field` from Task 1.
- Produces: immutable `CandidateEvidence`.
- Produces: `load_candidate_evidence(root: pathlib.Path, evidence: pathlib.Path) -> CandidateEvidence`.
- Produces: `prepare_body(body: str, candidate: CandidateEvidence) -> str`.
- Does not invoke GitHub in this task.

- [ ] **Step 1: Write failing evidence-freshness tests**

Create `developer/test_pr_publisher.py` with helpers that write a clean candidate
manifest and gate ledgers:

```python
import json
import pathlib
import tempfile
import unittest
from unittest import mock

from developer.pr_publisher import (
    CandidateEvidence,
    load_candidate_evidence,
    prepare_body,
)


class PRPublisherEvidenceTest(unittest.TestCase):
    def test_load_candidate_rejects_stale_gate_report(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            evidence = root / "build" / "evidence"
            evidence.mkdir(parents=True)
            (evidence / "candidate.json").write_text(json.dumps({
                "schemaVersion": 1,
                "commit": "a" * 40,
                "dirty": False,
                "candidateSha256": "1" * 64,
            }), encoding="utf-8")
            (evidence / "test-gates.json").write_text(json.dumps({
                "schemaVersion": 1,
                "suite": "test",
                "candidateSha256": "2" * 64,
                "gates": [{"name": "unit", "result": "passed"}],
            }), encoding="utf-8")
            with mock.patch(
                "developer.pr_publisher.git_output",
                return_value="a" * 40,
            ):
                candidate = load_candidate_evidence(root, evidence)
            self.assertNotIn("unit", candidate.passed)
            self.assertIn("test: stale candidate evidence", candidate.missing)

    def test_prepare_body_updates_only_machine_fields(self):
        candidate = CandidateEvidence(
            sha256="1" * 64,
            passed=("RCC local candidate", "test/unit"),
            failed=(),
            missing=("robot: not run",),
        )
        body = prepare_body(COMPLETE_BODY, candidate)
        self.assertIn("- Candidate SHA-256: " + "1" * 64, body)
        self.assertIn("- Passed gates: RCC local candidate, test/unit", body)
        self.assertIn("Durable learning captured: existing author claim", body)
```

Define `COMPLETE_BODY` in the test with all canonical fields and valid author
values.

- [ ] **Step 2: Run tests and confirm the missing module failure**

Run:

```sh
python3 -m unittest developer.test_pr_publisher -v
```

Expected: fail because `developer.pr_publisher` does not exist.

- [ ] **Step 3: Implement evidence loading and body preparation**

Create the pure portion of `developer/pr_publisher.py`:

```python
import dataclasses
import json
import pathlib
import subprocess

from developer.pr_receipt import replace_field, validate_body


@dataclasses.dataclass(frozen=True)
class CandidateEvidence:
    sha256: str
    passed: tuple[str, ...]
    failed: tuple[str, ...]
    missing: tuple[str, ...]


def git_output(root: pathlib.Path, *arguments: str) -> str:
    completed = subprocess.run(
        ("git", *arguments),
        cwd=root,
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if completed.returncode:
        raise RuntimeError(completed.stderr.strip())
    return completed.stdout.strip()


def load_candidate_evidence(
    root: pathlib.Path,
    evidence: pathlib.Path,
) -> CandidateEvidence:
    candidate = json.loads((evidence / "candidate.json").read_text(encoding="utf-8"))
    commit = git_output(root, "rev-parse", "HEAD")
    digest = candidate.get("candidateSha256")
    if candidate.get("schemaVersion") != 1 or candidate.get("dirty") is not False:
        raise RuntimeError("candidate manifest is not a clean schemaVersion 1 candidate")
    if candidate.get("commit") != commit:
        raise RuntimeError("candidate manifest does not describe current HEAD; run RCC local")
    if not isinstance(digest, str) or len(digest) != 64:
        raise RuntimeError("candidate manifest lacks a SHA-256 digest")

    passed = ["RCC local candidate"]
    failed = []
    missing = []
    for suite in ("test", "robot"):
        path = evidence / f"{suite}-gates.json"
        if not path.exists():
            missing.append(f"{suite}: not run")
            continue
        report = json.loads(path.read_text(encoding="utf-8"))
        if report.get("candidateSha256") != digest:
            missing.append(f"{suite}: stale candidate evidence")
            continue
        for gate in report.get("gates", []):
            name = f"{suite}/{gate.get('name', 'unnamed')}"
            result = gate.get("result")
            if result == "passed":
                passed.append(name)
            elif result == "failed":
                failed.append(name)
            else:
                missing.append(f"{name}: {result or 'missing result'}")
    return CandidateEvidence(digest, tuple(passed), tuple(failed), tuple(missing))


def prepare_body(body: str, candidate: CandidateEvidence) -> str:
    replacements = {
        "Passed gates:": ", ".join(candidate.passed),
        "Failed gates:": ", ".join(candidate.failed) or "none recorded",
        "Missing or skipped gates:": ", ".join(candidate.missing) or "none",
        "Candidate SHA-256:": candidate.sha256,
    }
    rendered = body
    for label, value in replacements.items():
        rendered = replace_field(rendered, label, value)
    validate_body(rendered)
    return rendered
```

Keep malformed JSON, missing files, missing gate arrays, and non-terminal gate
results fail-closed with errors that name the file or suite.

- [ ] **Step 4: Run evidence tests and make them pass**

Run:

```sh
python3 -m unittest developer.test_pr_publisher.PRPublisherEvidenceTest -v
```

Expected: all evidence aggregation tests pass.

- [ ] **Step 5: Add malformed and terminal-result regressions**

Add tests proving:

- a candidate whose commit differs from `git rev-parse HEAD` is rejected;
- `dirty: true` is rejected;
- a matching failed gate is written only to `Failed gates`;
- `missing`, `skipped`, and `gated` results are written only to
  `Missing or skipped gates`;
- author fields remain byte-identical after machine-field replacement.

Run:

```sh
python3 -m unittest developer.test_pr_publisher.PRPublisherEvidenceTest -v
```

Expected: all tests pass.

- [ ] **Step 6: Commit evidence preparation**

```sh
git add developer/pr_publisher.py developer/test_pr_publisher.py
git commit -m "feat: prepare current-candidate PR receipts"
```

---

### Task 3: Validated GitHub publication

**Files:**
- Modify: `developer/pr_publisher.py`
- Modify: `developer/test_pr_publisher.py`

**Interfaces:**
- Consumes: `load_candidate_evidence` and `prepare_body` from Task 2.
- Produces: `publish(root: pathlib.Path, evidence: pathlib.Path, environment: dict[str, str]) -> str`, returning the persisted PR URL.
- GitHub boundary: exact `gh pr list`, `gh pr create`, `gh pr edit`, and `gh pr view` argv; never a shell command.

- [ ] **Step 1: Write failing fake-`gh` publication tests**

Add a temporary executable named `gh` to the test `PATH`. The fake reads and
writes a JSON state file named by `FAKE_GH_STATE`, records argv, and implements
the four PR subcommands used by the publisher.

Add these tests:

```python
def test_publish_stops_before_gh_write_when_body_is_incomplete(self):
    fixture = PublisherFixture(self)
    fixture.write_candidate()
    fixture.write_body(COMPLETE_BODY.replace(
        "Real-tool evidence: not run",
        "Real-tool evidence:",
    ))
    with self.assertRaisesRegex(RuntimeError, "Real-tool evidence"):
        fixture.publish()
    self.assertNotIn("create", fixture.mutating_commands())
    self.assertNotIn("edit", fixture.mutating_commands())


def test_publish_repairs_existing_pr_and_preserves_prose(self):
    fixture = PublisherFixture(self)
    fixture.write_candidate()
    fixture.seed_existing_pr(COMPLETE_BODY)
    url = fixture.publish()
    self.assertEqual(url, "https://github.test/example/camp/pull/7")
    persisted = fixture.persisted_body()
    self.assertIn("existing behavior prose", persisted)
    self.assertEqual(persisted.count("Candidate SHA-256:"), 1)


def test_publish_creates_then_reads_back_exact_body(self):
    fixture = PublisherFixture(self)
    fixture.write_candidate()
    fixture.write_body(COMPLETE_BODY)
    fixture.publish()
    self.assertEqual(fixture.commands_named("create"), 1)
    self.assertEqual(fixture.commands_named("view"), 1)
```

- [ ] **Step 2: Run publication tests and verify they fail**

Run:

```sh
python3 -m unittest developer.test_pr_publisher -v
```

Expected: publication tests fail because `publish` is not implemented.

- [ ] **Step 3: Implement repository and GitHub preconditions**

Add helpers to `developer/pr_publisher.py`:

```python
def run_command(root, environment, arguments):
    completed = subprocess.run(
        arguments,
        cwd=root,
        env=environment,
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if completed.returncode:
        raise RuntimeError(
            f"{' '.join(arguments[:3])} failed: {completed.stderr.strip()}"
        )
    return completed.stdout


def require_publishable_branch(root):
    branch = git_output(root, "symbolic-ref", "--short", "HEAD")
    if not branch:
        raise RuntimeError("pull request publication requires a named branch")
    if git_output(root, "status", "--porcelain", "--untracked-files=all"):
        raise RuntimeError("pull request publication requires a clean checkout")
    upstream = git_output(root, "rev-parse", "--abbrev-ref", "@{upstream}")
    counts = git_output(root, "rev-list", "--left-right", "--count", f"{upstream}...HEAD")
    if counts != "0\t0":
        raise RuntimeError("current branch must be pushed exactly before PR publication")
    return branch
```

Parse `gh pr list --head BRANCH --state open --json number,url,body,title` as a
JSON array and reject more than one result.

- [ ] **Step 4: Implement validation-last create/edit and readback**

Implement `publish` with this order:

```python
def publish(root, evidence, environment):
    branch = require_publishable_branch(root)
    candidate = load_candidate_evidence(root, evidence)
    existing = list_open_pull_requests(root, environment, branch)
    body_path = evidence / "pr-body.md"
    if existing:
        source_body = existing[0]["body"]
    elif body_path.exists():
        source_body = body_path.read_text(encoding="utf-8")
    else:
        template = root / ".github" / "pull_request_template.md"
        body_path.parent.mkdir(parents=True, exist_ok=True)
        body_path.write_text(template.read_text(encoding="utf-8"), encoding="utf-8")
        raise RuntimeError(f"complete {body_path}, then rerun the RCC pr task")

    rendered = prepare_body(source_body, candidate)
    body_path.write_text(rendered, encoding="utf-8")
    validate_body(rendered)
    if existing:
        number = str(existing[0]["number"])
        run_command(root, environment, [
            "gh", "pr", "edit", number, "--body-file", str(body_path),
        ])
    else:
        title = environment.get("CAMP_PR_TITLE") or git_output(
            root, "show", "-s", "--format=%s", "HEAD"
        )
        base = environment.get("CAMP_PR_BASE") or repository_default_branch(
            root, environment
        )
        created = run_command(root, environment, [
            "gh", "pr", "create", "--base", base, "--head", branch,
            "--title", title, "--body-file", str(body_path),
        ]).strip()
        number = pull_request_number(created)

    persisted = read_pull_request(root, environment, number)
    validate_body(persisted["body"])
    if persisted["body"].rstrip() != rendered.rstrip():
        raise RuntimeError("persisted pull request body differs from validated body")
    return persisted["url"]
```

Use `gh repo view --json defaultBranchRef` for the default base and accept only
an HTTPS GitHub PR URL from `gh pr create`.

- [ ] **Step 5: Add failure and idempotency tests**

Add tests proving:

- duplicate open PRs fail before edit;
- `gh pr create` and `gh pr edit` failures remain nonzero;
- a readback mismatch fails without reporting the URL;
- rerunning after a successful create performs one edit, not a second create;
- `CAMP_PR_TITLE` and `CAMP_PR_BASE` are honored;
- the fake secret value `SECRET_SENTINEL_123` in the environment never appears
  in the body, state file body, or command arguments.

Run:

```sh
python3 -m unittest developer.test_pr_publisher -v
```

Expected: all publication tests pass.

- [ ] **Step 6: Commit GitHub publication**

```sh
git add developer/pr_publisher.py developer/test_pr_publisher.py
git commit -m "feat: publish validated pull request receipts"
```

---

### Task 4: RCC task, agent mandate, and operational guide

**Files:**
- Modify: `tasks.py`
- Modify: `developer/toolkit.yaml`
- Modify: `developer/test_tasks.py`
- Modify: `AGENTS.md`
- Modify: `docs/skills/testing-release-evidence.md`
- Modify: `releasepipeline/documentation_contract_test.go`

**Interfaces:**
- Consumes: `pr_publisher.publish(ROOT, EVIDENCE, dict(os.environ))`.
- Produces: RCC development task `pr`.
- Produces: exact agent command `rcc run -r developer/toolkit.yaml --dev -t pr`.

- [ ] **Step 1: Write failing task and documentation contract tests**

Add to `developer/test_tasks.py`:

```python
def test_pr_task_delegates_to_repository_publisher(self):
    with mock.patch("tasks.pr_publisher.publish", return_value="https://github.test/pr/7") as publish:
        tasks.pr_task.body(None)
    publish.assert_called_once_with(tasks.ROOT, tasks.EVIDENCE, mock.ANY)
```

Extend `TestSelfImprovementReceiptRemainsReviewable` or add a focused test in
`releasepipeline/documentation_contract_test.go` requiring these strings:

```go
requireContains(t, agents,
    "rcc run -r developer/toolkit.yaml --dev -t pr",
    "must not use `gh pr create` or `gh pr edit` directly",
)
requireContains(t, toolkit,
    "pr:",
    "python call_invoke.py pr",
)
requireContains(t, publisher,
    "validate_body",
    `"gh", "pr", "create"`,
    `"gh", "pr", "edit"`,
)
```

- [ ] **Step 2: Run focused tests and confirm missing wiring**

Run:

```sh
python3 -m unittest developer.test_tasks.TasksTest.test_pr_task_delegates_to_repository_publisher -v
go test ./releasepipeline -run 'Test.*PullRequest.*Publisher|TestSelfImprovement' -count=1
```

Expected: fail because the task, toolkit entry, and agent mandate are absent.

- [ ] **Step 3: Wire the Invoke and RCC tasks**

In `tasks.py`, import `developer.pr_publisher` and add:

```python
@task(name="pr")
def pr_task(_context):
    """Create or update a PR only after validating its evidence receipt."""
    url = pr_publisher.publish(ROOT, EVIDENCE, dict(os.environ))
    print(url)
```

In `developer/toolkit.yaml`, add:

```yaml
  pr:
    shell: python call_invoke.py pr
```

under `devTasks`.

- [ ] **Step 4: Add the agent publication mandate**

Add a short subsection to `AGENTS.md` under commit and pull-request guidance:

```markdown
### Pull-request publication

Agents must create or update Camp pull-request bodies through:

`rcc run -r developer/toolkit.yaml --dev -t pr`

Agents must not use `gh pr create` or `gh pr edit` directly. The RCC task
preserves prose, binds current-candidate evidence, validates every canonical
receipt field before mutation, and verifies the persisted body. Read-only
`ghx pr view` and `ghx pr checks` remain the preferred inspection path.
```

- [ ] **Step 5: Replace provisional guide text with implemented procedure**

Update `docs/skills/testing-release-evidence.md` so the durable procedure says:

```markdown
Before PR publication, run `rcc local` or the repository wrapper that produces
`build/evidence/candidate.json`, complete `build/evidence/pr-body.md`, push the
named branch, then run:

`rcc run -r developer/toolkit.yaml --dev -t pr`

The task consumes a gate ledger only when its candidate SHA-256 matches the
current clean candidate. It preserves non-receipt prose, refuses empty,
placeholder, duplicate, stale, or invalid fields before GitHub mutation, and
validates the persisted body afterward. A validated or persisted body is not
evidence that CI passed, the PR merged, master is green, or a release occurred.
```

Retain the observed warning that raw `gh pr create --body-file` bypasses the
repository template, but point to the RCC task as the prevention mechanism.

- [ ] **Step 6: Run task, documentation, and release-pipeline tests**

Run:

```sh
python3 -m unittest discover -s developer -p 'test_*.py' -v
go test ./docs ./releasepipeline -count=1
git diff --check
```

Expected: all tests pass.

- [ ] **Step 7: Commit RCC wiring and documentation**

```sh
git add tasks.py developer/toolkit.yaml developer/test_tasks.py AGENTS.md \
  docs/skills/testing-release-evidence.md \
  releasepipeline/documentation_contract_test.go
git commit -m "feat: add RCC pull request publisher"
```

---

### Task 5: Full verification and dogfood receipt

**Files:**
- Modify only if a verification failure identifies a defect in a Task 1–4 file.
- Generate ignored evidence: `build/evidence/candidate.json`,
  `build/evidence/test-gates.json`, and `build/evidence/pr-body.md`.

**Interfaces:**
- Consumes: completed RCC `pr` task and current pushed branch.
- Produces: exact local verification receipt and, when publication is authorized, one validated live PR body.

- [ ] **Step 1: Run the focused red/green suites once more**

```sh
python3 -m unittest \
  developer.test_pr_receipt \
  developer.verify_pr_receipt_test \
  developer.test_pr_publisher \
  developer.test_tasks -v
go test ./releasepipeline -run 'Test.*PullRequest|TestSelfImprovement' -count=1
```

Expected: all tests pass.

- [ ] **Step 2: Run repository verification**

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build ./cmd/camp
git diff --check
```

Expected: all applicable commands pass.

- [ ] **Step 3: Run the RCC source gate**

```sh
rcc run -r developer/toolkit.yaml --dev -t test --silent
```

Expected: `build/evidence/candidate.json` and
`build/evidence/test-gates.json` share one candidate SHA-256 and every mandatory
source gate is terminal and passed.

- [ ] **Step 4: Prove stale Robot evidence is not reused**

If `build/evidence/robot-gates.json` exists with a different candidate digest,
run the pure publisher inspection test or invoke the preparation function in
the RCC environment and confirm `Missing or skipped gates` reports
`robot: stale candidate evidence`. Do not edit the stale report to make it
match.

- [ ] **Step 5: Prepare the implementation PR body**

Write `build/evidence/pr-body.md` from the canonical template with:

- behavior and safety impact describing pre-publication validation;
- `Real-tool evidence:` naming the exact RCC `test` result and whether Robot
  was run;
- `Release-note classification: docs/changelog.md` if the implementation
  changes `docs/changelog.md`, otherwise an explicit
  `no-release-note: internal contributor workflow` classification;
- the complete documentation receipt for
  `docs/skills/testing-release-evidence.md`;
- publication state separating implemented, tested, pushed, CI, merged,
  master-green, and released.

Leave machine fields present but allow the task to replace their values from
current evidence.

- [ ] **Step 6: Push the exact implementation branch**

```sh
git status --short --branch
git push -u origin HEAD
```

Expected: the checkout is clean and local HEAD equals its upstream.

- [ ] **Step 7: Dogfood the RCC publisher**

```sh
rcc run -r developer/toolkit.yaml --dev -t pr --silent
```

Expected: the task creates or updates exactly one PR, prints its HTTPS URL, and
readback validation succeeds. Inspect with:

```sh
ghx pr view --json url,body,headRefOid
```

Confirm the live body contains every canonical label exactly once and the
candidate digest equals `build/evidence/candidate.json`.

- [ ] **Step 8: Report exact state**

Report:

- implementation commits and pushed ref;
- focused, full, race, vet, build, diff, and RCC results;
- PR URL and current checks;
- whether the task created or edited the PR;
- documentation receipt;
- separate values for pushed, CI-green, merged, master-green, packaged,
  released, deployed, and dogfooded.
