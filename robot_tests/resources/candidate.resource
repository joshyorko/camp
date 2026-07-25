*** Settings ***
Library    OperatingSystem
Library    Process

*** Variables ***
${REPOSITORY_ROOT}        ${CURDIR}${/}..${/}..
${CAMP_BINARY}            %{CAMP_TEST_BINARY}
${CANDIDATE_MANIFEST}     ${REPOSITORY_ROOT}${/}build${/}evidence${/}candidate.json
${OWNERSHIP_MARKER}       .camp-robot-owned

*** Keywords ***
Resolve Candidate Inputs
    ${binary}=    Evaluate
    ...    os.path.abspath($CAMP_BINARY if os.path.isabs($CAMP_BINARY) else os.path.join($REPOSITORY_ROOT, $CAMP_BINARY))
    ...    modules=os
    ${manifest}=    Evaluate    os.path.abspath($CANDIDATE_MANIFEST)    modules=os
    Set Suite Variable    ${CAMP_BINARY}    ${binary}
    Set Suite Variable    ${CANDIDATE_MANIFEST}    ${manifest}
    File Should Exist    ${CAMP_BINARY}
    File Should Exist    ${CANDIDATE_MANIFEST}

Prepare Isolated Controller
    ${root}=    Evaluate    tempfile.mkdtemp(prefix="camp-robot-")    modules=tempfile
    Set Test Variable    ${TEST_ROOT}    ${root}
    Set Test Variable    ${HOME_ROOT}    ${TEST_ROOT}${/}home
    Set Test Variable    ${XDG_CONFIG_HOME}    ${TEST_ROOT}${/}xdg${/}config
    Set Test Variable    ${XDG_DATA_HOME}    ${TEST_ROOT}${/}xdg${/}data
    Set Test Variable    ${XDG_STATE_HOME}    ${TEST_ROOT}${/}xdg${/}state
    Set Test Variable    ${XDG_CACHE_HOME}    ${TEST_ROOT}${/}xdg${/}cache
    Set Test Variable    ${SOURCES_ROOT}    ${TEST_ROOT}${/}sources
    Set Test Variable    ${BACKEND_ROOT}    ${TEST_ROOT}${/}backend
    Create File    ${TEST_ROOT}${/}${OWNERSHIP_MARKER}
    Create Directory    ${HOME_ROOT}
    Create Directory    ${XDG_CONFIG_HOME}
    Create Directory    ${XDG_DATA_HOME}
    Create Directory    ${XDG_STATE_HOME}
    Create Directory    ${XDG_CACHE_HOME}
    Create Directory    ${SOURCES_ROOT}
    Create Directory    ${BACKEND_ROOT}

Remove Isolated Controller
    File Should Exist    ${TEST_ROOT}${/}${OWNERSHIP_MARKER}
    Should Contain    ${TEST_ROOT}    camp-robot-
    Remove Directory    ${TEST_ROOT}    recursive=True

Run Candidate
    [Arguments]    ${working_directory}    @{arguments}
    ${result}=    Run Process
    ...    ${CAMP_BINARY}
    ...    @{arguments}
    ...    cwd=${working_directory}
    ...    env:HOME=${HOME_ROOT}
    ...    env:XDG_CONFIG_HOME=${XDG_CONFIG_HOME}
    ...    env:XDG_DATA_HOME=${XDG_DATA_HOME}
    ...    env:XDG_STATE_HOME=${XDG_STATE_HOME}
    ...    env:XDG_CACHE_HOME=${XDG_CACHE_HOME}
    RETURN    ${result}

Parse Candidate JSON
    [Arguments]    ${result}
    ${output}=    Evaluate    ($result.stdout or $result.stderr).strip()
    ${payload}=    Evaluate    json.loads($output)    modules=json
    RETURN    ${payload}

Initialize Isolated Camp
    [Arguments]    ${name}    ${source_root}
    Create Directory    ${source_root}
    ${result}=    Run Candidate
    ...    ${TEST_ROOT}
    ...    --json
    ...    init
    ...    ${source_root}
    ...    --name
    ...    ${name}
    ...    --backend
    ...    file://${BACKEND_ROOT}
    ...    --workspace-provider
    ...    docker
    ...    --workspace-context
    ...    default
    Should Be Equal As Integers    ${result.rc}    0
    ${payload}=    Parse Candidate JSON    ${result}
    Should Be Equal As Integers    ${payload}[schemaVersion]    1
    Should Be Equal    ${payload}[kind]    init
    RETURN    ${payload}

Candidate Completion Should Contain
    [Arguments]    ${shell}    ${marker}
    ${result}=    Run Candidate    ${TEST_ROOT}    completion    ${shell}
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    ${marker}
    Should Not Be Empty    ${result.stdout}
