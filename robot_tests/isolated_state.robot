*** Settings ***
Resource         resources/candidate.resource
Suite Setup      Resolve Candidate Inputs
Test Setup       Prepare Isolated Controller
Test Teardown    Remove Isolated Controller

*** Test Cases ***
Two Source Roots Initialize Without Sharing Manifests
    [Tags]    req:CAMP-BB-INIT-001
    ${alpha_root}=    Set Variable    ${SOURCES_ROOT}${/}alpha
    ${beta_root}=    Set Variable    ${SOURCES_ROOT}${/}beta
    ${alpha}=    Initialize Isolated Camp    alpha    ${alpha_root}
    ${beta}=    Initialize Isolated Camp    beta    ${beta_root}
    Should Be Equal    ${alpha}[result][manifest][id]    alpha
    Should Be Equal    ${beta}[result][manifest][id]    beta
    Should Be Equal
    ...    ${alpha}[result][manifestPath]
    ...    ${alpha_root}${/}.camp${/}camp.yaml
    Should Be Equal
    ...    ${beta}[result][manifestPath]
    ...    ${beta_root}${/}.camp${/}camp.yaml
    File Should Exist    ${alpha_root}${/}.camp${/}camp.yaml
    File Should Exist    ${beta_root}${/}.camp${/}camp.yaml
    Should Not Be Equal
    ...    ${alpha}[result][manifestPath]
    ...    ${beta}[result][manifestPath]

Initialized Camps Are Not Reported As Stored Sessions
    [Tags]    req:CAMP-BB-STATE-001
    ${alpha_root}=    Set Variable    ${SOURCES_ROOT}${/}alpha
    Initialize Isolated Camp    alpha    ${alpha_root}
    ${listed}=    Run Candidate    ${TEST_ROOT}    --json    list
    Should Be Equal As Integers    ${listed.rc}    0
    ${list_payload}=    Parse Candidate JSON    ${listed}
    Should Be Equal As Integers    ${list_payload}[schemaVersion]    1
    Should Be Equal    ${list_payload}[kind]    list
    Should Be Empty    ${list_payload}[result]
    ${status}=    Run Candidate
    ...    ${alpha_root}
    ...    --json
    ...    status
    ...    --camp
    ...    alpha
    Should Not Be Equal As Integers    ${status.rc}    0
    ${status_payload}=    Parse Candidate JSON    ${status}
    Should Be Equal    ${status_payload}[error][code]    command_failed
    Should Be Equal
    ...    ${status_payload}[error][message]
    ...    no matching Camp session; next: camp list

Recover Without A Session Returns Exact Safe Next Step
    [Tags]    req:CAMP-BB-RECOVERY-001
    ${alpha_root}=    Set Variable    ${SOURCES_ROOT}${/}alpha
    Initialize Isolated Camp    alpha    ${alpha_root}
    ${result}=    Run Candidate
    ...    ${alpha_root}
    ...    --json
    ...    recover
    ...    --camp
    ...    alpha
    Should Not Be Equal As Integers    ${result.rc}    0
    ${payload}=    Parse Candidate JSON    ${result}
    Should Be Equal As Integers    ${payload}[schemaVersion]    1
    Should Be Equal    ${payload}[kind]    error
    Should Be Equal    ${payload}[error][code]    command_failed
    Should Be Equal
    ...    ${payload}[error][message]
    ...    no matching Camp session; next: camp list
