*** Settings ***
Resource         resources/full_cli.resource
Suite Setup      Resolve Candidate Inputs
Test Setup       Prepare Isolated Controller
Test Teardown    Remove Isolated Controller

*** Test Cases ***
The Entire Public Command Tree Is Present And Callable
    [Tags]    req:CAMP-BB-COMMAND-SURFACE-001
    Every Public Command Should Expose Help

Machine Setup Uses The Pinned Toolchain
    [Tags]    req:CAMP-BB-SETUP-001
    Machine Setup Should Resolve The Real Managed Tools

Read Only And Machine Configuration Commands Execute
    [Tags]    req:CAMP-BB-SAFE-COMMANDS-001
    Safe Isolated Commands Should Execute Their Production Handlers

Lifecycle Commands Reject Missing State Consistently
    [Tags]    req:CAMP-BB-LIFECYCLE-ERRORS-001
    Lifecycle Commands Without A Session Should Fail Truthfully
