@echo off
setlocal

set "PACKAGE_DIR=%~dp0"
set "PAYLOAD=%PACKAGE_DIR%payload\goaster.exe"

if not exist "%PAYLOAD%" (
    echo GoAster payload is missing: "%PAYLOAD%"
    exit /b 1
)

rem Running the payload directly keeps this console attached for interactive use.
"%PAYLOAD%" %*
set "EXIT_CODE=%ERRORLEVEL%"
endlocal & exit /b %EXIT_CODE%
