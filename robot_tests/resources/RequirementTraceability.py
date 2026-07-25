import json
from pathlib import Path

from robot.api import TestSuiteBuilder


class RequirementTraceability:
    ROBOT_LIBRARY_SCOPE = "GLOBAL"

    def validate_requirement_mapping(
        self, manifest_path: str, suites_root: str, claim_scope: str = "pr"
    ) -> str:
        manifest = json.loads(Path(manifest_path).read_text(encoding="utf-8"))
        if manifest.get("schemaVersion") != 1:
            raise AssertionError("requirements manifest schemaVersion must be 1")

        requirements = manifest.get("requirements", [])
        by_id = {}
        errors = []
        for requirement in requirements:
            requirement_id = requirement.get("id", "")
            if not requirement_id or requirement_id in by_id:
                errors.append(f"missing or duplicate requirement ID: {requirement_id!r}")
                continue
            by_id[requirement_id] = requirement
            tier = requirement.get("tier")
            if tier not in {"required-now", "roadmap-gated", "superseded"}:
                errors.append(f"{requirement_id}: unknown tier {tier!r}")
            if not requirement.get("source") or not requirement.get("summary"):
                errors.append(f"{requirement_id}: source and summary are required")
            if tier == "roadmap-gated" and not requirement.get("gate"):
                errors.append(f"{requirement_id}: roadmap-gated requirement needs gate")
            if tier == "superseded" and not requirement.get("supersededBy"):
                errors.append(f"{requirement_id}: superseded requirement needs supersededBy")

        suite = TestSuiteBuilder().build(str(Path(suites_root)))
        tagged_tests = {requirement_id: [] for requirement_id in by_id}
        for test in self._tests(suite):
            requirement_tags = [
                str(tag)[4:] for tag in test.tags if str(tag).startswith("req:")
            ]
            if not requirement_tags:
                errors.append(f"{test.longname}: missing req:<ID> tag")
                continue
            for requirement_id in requirement_tags:
                requirement = by_id.get(requirement_id)
                if requirement is None:
                    errors.append(f"{test.longname}: unknown requirement {requirement_id}")
                    continue
                tagged_tests[requirement_id].append(test.longname)
                if requirement["tier"] != "required-now":
                    errors.append(
                        f"{test.longname}: {requirement['tier']} requirement "
                        f"{requirement_id} must not execute"
                    )

        for requirement_id, requirement in by_id.items():
            if requirement["tier"] == "required-now" and not tagged_tests[requirement_id]:
                errors.append(f"{requirement_id}: required-now requirement is unmapped")

        roadmap = [
            {
                "id": requirement_id,
                "gate": requirement["gate"],
                "summary": requirement["summary"],
            }
            for requirement_id, requirement in by_id.items()
            if requirement["tier"] == "roadmap-gated"
        ]
        if claim_scope == "product" and roadmap:
            errors.append(
                "product claim blocked by roadmap-gated requirements: "
                + ", ".join(item["id"] for item in roadmap)
            )
        elif claim_scope != "pr":
            errors.append(f"unknown requirements claim scope: {claim_scope!r}")

        if errors:
            raise AssertionError("\n".join(errors))

        summary = {
            "schemaVersion": 1,
            "claimScope": claim_scope,
            "requiredNow": sorted(
                requirement_id
                for requirement_id, requirement in by_id.items()
                if requirement["tier"] == "required-now"
            ),
            "roadmapGated": roadmap,
            "superseded": sorted(
                requirement_id
                for requirement_id, requirement in by_id.items()
                if requirement["tier"] == "superseded"
            ),
        }
        return json.dumps(summary, sort_keys=True)

    def _tests(self, suite):
        yield from suite.tests
        for child in suite.suites:
            yield from self._tests(child)
