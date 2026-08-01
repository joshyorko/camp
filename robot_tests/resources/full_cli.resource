*** Settings ***
Resource    candidate.resource

*** Variables ***
@{PUBLIC_COMMANDS}
...    attach
...    close
...    completion
...    config
...    config set
...    config show
...    doctor
...    images
...    images capture
...    images list
...    images restore
...    init
...    kit
...    kit export
...    kit import
...    kit inspect
...    kit verify
...    list
...    open
...    provider
...    provider add
...    provider list
...    provider use
...    recover
...    reopen
...    serve
...    serve logs
...    serve restart
...    serve status
...    setup
...    status
...    strike
...    sync

*** Keywords ***
Every Public Command Should Expose Help
    FOR    ${command_path}    IN    @{PUBLIC_COMMANDS}
        @{arguments}=    Evaluate    $command_path.split() + ["--help"]
        ${result}=    Run Candidate    ${TEST_ROOT}    @{arguments}
        Should Be Equal As Integers    ${result.rc}    0    ${command_path}: ${result.stderr}
        Should Contain    ${result.stdout}    Usage:
    END

Machine Setup Should Resolve The Real Managed Tools
    ${result}=    Run Candidate    ${TEST_ROOT}    --json    setup
    Should Be Equal As Integers    ${result.rc}    0    ${result.stderr}
    ${payload}=    Parse Candidate JSON    ${result}
    Should Be Equal As Integers    ${payload}[schemaVersion]    1
    Should Be Equal    ${payload}[kind]    setup
    Length Should Be    ${payload}[result][tools]    2

Safe Isolated Commands Should Execute Their Production Handlers
    ${show}=    Run Candidate    ${TEST_ROOT}    --json    config    show
    Should Be Equal As Integers    ${show.rc}    0    ${show.stderr}
    ${set}=    Run Candidate    ${TEST_ROOT}    --json    config    set    devpodContext    default
    Should Be Equal As Integers    ${set.rc}    0    ${set.stderr}
    ${providers}=    Run Candidate    ${TEST_ROOT}    --json    provider    list
    Should Be Equal As Integers    ${providers.rc}    0    ${providers.stderr}
    ${doctor}=    Run Candidate    ${TEST_ROOT}    --json    doctor
    Should Not Be Equal As Integers    ${doctor.rc}    2
Lifecycle Commands Without A Session Should Fail Truthfully
    FOR    ${command_path}    IN    open    attach    sync    close    reopen    recover    status    images capture    images list    images restore    serve status registry    serve logs registry    serve restart registry
        @{arguments}=    Evaluate    ["--json"] + $command_path.split()
        ${result}=    Run Candidate    ${TEST_ROOT}    @{arguments}
        Should Not Be Equal As Integers    ${result.rc}    0    ${command_path} unexpectedly succeeded
        ${payload}=    Parse Candidate JSON    ${result}
        Should Be Equal As Integers    ${payload}[schemaVersion]    1
        Should Be Equal    ${payload}[kind]    error
    END
