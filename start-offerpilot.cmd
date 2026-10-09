@echo off
rem Compatibility alias for the original launcher name.
setlocal
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0start-aide.ps1" %*
set "RC=%ERRORLEVEL%"
if "%~1"=="" (
  echo.
  pause
)
endlocal & exit /b %RC%

