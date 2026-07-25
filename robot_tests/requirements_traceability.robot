*** Settings ***
Library          Collections
Library          OperatingSystem
Library          resources/RequirementTraceability.py

*** Variables ***
${REQUIREMENTS_MANIFEST}    ${CURDIR}${/}resources${/}requirements.json
${REQUIREMENTS_SCOPE}       %{CAMP_REQUIREMENTS_SCOPE=pr}
@{APPROVED_ROADMAP_REQUIREMENTS}
...    CAMP-PROOF-OPEN-001
...    CAMP-PROOF-ATTACH-001
...    CAMP-PROOF-SYNC-001
...    CAMP-PROOF-CLOSE-001
...    CAMP-PROOF-FRESH-CONTROLLER-REOPEN-001
...    CAMP-PROOF-MULTI-CAMP-ROUTING-001
...    CAMP-PROOF-FILESYSTEM-SEMANTICS-001
...    CAMP-PROOF-OCI-DIGEST-PORTABILITY-001
...    CAMP-PROOF-MINIO-FRESH-CONTROLLER-001
...    CAMP-PROOF-TWO-WRITER-CAS-001
...    CAMP-PROOF-ORPHAN-GENERATION-RECOVERY-001
...    CAMP-PROOF-CRASH-MATRIX-001
...    CAMP-PROOF-SUPERVISOR-RECOVERY-001
...    CAMP-PROOF-FORWARDER-RECOVERY-001
...    CAMP-PROOF-STALE-LEASE-RECOVERY-001
...    CAMP-PROOF-EXACT-CLEANUP-001
...    CAMP-PROOF-UNRELATED-WORKSPACE-001
...    CAMP-PROOF-PRIVATE-DEVPOD-CONTEXT-001
...    CAMP-PROOF-LOOPBACK-PORT-OWNERSHIP-001
...    CAMP-PROOF-JSON-SUCCESS-RECEIPT-001
...    CAMP-PROOF-JSON-PROGRESS-RECEIPT-001
...    CAMP-PROOF-JSON-RECOVERY-RECEIPT-001
...    CAMP-PROOF-ROOM-WOLFI-MATRIX-001
...    CAMP-PROOF-KUBERNETES-PROTECTED-001
...    CAMP-PROOF-DOCS-HELP-COMPLETION-PARITY-001
...    CAMP-PROOF-CANDIDATE-CONSISTENCY-001

*** Test Cases ***
Robot Requirements Have Honest Executable Coverage
    [Tags]    req:CAMP-BB-TRACE-001
    ${summary}=    Validate Requirement Mapping
    ...    ${REQUIREMENTS_MANIFEST}
    ...    ${CURDIR}
    ...    ${REQUIREMENTS_SCOPE}
    Log    ${summary}

Roadmap Report Covers Approved Product Proof Lanes
    [Tags]    req:CAMP-BB-TRACE-001
    ${summary}=    Validate Requirement Mapping
    ...    ${REQUIREMENTS_MANIFEST}
    ...    ${CURDIR}
    ...    pr
    ${report}=    Evaluate    json.loads($summary)    modules=json
    ${roadmap_ids}=    Evaluate
    ...    [item["id"] for item in $report["roadmapGated"]]
    FOR    ${requirement_id}    IN    @{APPROVED_ROADMAP_REQUIREMENTS}
        List Should Contain Value    ${roadmap_ids}    ${requirement_id}
    END

Product Scope Rejects Every Reported Roadmap Gate
    [Tags]    req:CAMP-BB-TRACE-001
    ${manifest_text}=    Get File    ${REQUIREMENTS_MANIFEST}
    ${manifest}=    Evaluate    json.loads($manifest_text)    modules=json
    ${roadmap_ids}=    Evaluate
    ...    [item["id"] for item in $manifest["requirements"] if item["tier"] == "roadmap-gated"]
    ${error}=    Run Keyword And Expect Error
    ...    *
    ...    Validate Requirement Mapping
    ...    ${REQUIREMENTS_MANIFEST}
    ...    ${CURDIR}
    ...    product
    FOR    ${requirement_id}    IN    @{roadmap_ids}
        Should Contain    ${error}    ${requirement_id}
    END
