*** Settings ***
Resource         resources/candidate.resource
Suite Setup      Resolve Candidate Inputs
Test Setup       Prepare Isolated Controller
Test Teardown    Remove Isolated Controller

*** Test Cases ***
Candidate Digest And Version Match The Build Receipt
    [Tags]    req:CAMP-BB-CANDIDATE-001
    ${manifest_text}=    Get File    ${CANDIDATE_MANIFEST}
    ${manifest}=    Evaluate    json.loads($manifest_text)    modules=json
    ${digest}=    Evaluate
    ...    hashlib.sha256(pathlib.Path($CAMP_BINARY).read_bytes()).hexdigest()
    ...    modules=hashlib,pathlib
    Should Be Equal    ${digest}    ${manifest}[candidateSha256]
    ${executable}=    Evaluate    os.access($CAMP_BINARY, os.X_OK)    modules=os
    Should Be True    ${executable}    Candidate is not executable: ${CAMP_BINARY}
    ${version}=    Run Candidate    ${TEST_ROOT}    --version
    Should Be Equal As Integers    ${version.rc}    0
    Should Contain    ${version.stdout}    camp version ${manifest}[version]
    Should Contain    ${version.stdout}    commit ${manifest}[commit]
    ${dirty}=    Evaluate    str($manifest["dirty"]).lower()
    Should Contain    ${version.stdout}    dirty ${dirty}
