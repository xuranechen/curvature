@echo off
setlocal EnableExtensions
cd /d "%~dp0"
title Curvature Client

set "EXE=%~dp0curvature.exe"
if not exist "%EXE%" (
  for /d %%D in ("%~dp0dist\curvature_*") do (
    if exist "%%D\curvature.exe" set "EXE=%%D\curvature.exe"
  )
)
if not exist "%EXE%" (
  echo [curvature] error: curvature.exe not found in %~dp0
  pause
  exit /b 1
)
echo [curvature] using: %EXE%
echo [curvature] checking status...

>nul 2>&1 "%EXE%" -stop
timeout /t 1 /nobreak >nul

set "LOG=%TEMP%\curvature_start_%RANDOM%.txt"
echo [curvature] starting service...

if exist "%~dp0config.json" (
  "%EXE%" -config "%~dp0config.json" -bind-relay . > "%LOG%" 2>&1
) else (
  "%EXE%" -bind-relay . > "%LOG%" 2>&1
)

type "%LOG%"
echo.

set "URL="
for /f "usebackq delims=" %%L in (`findstr /rc:"https\?://" "%LOG%" 2^>nul`) do (
  if not defined URL set "URL=%%L"
)
if defined URL (
  echo [curvature] opening: %URL%
  start "" "%URL%"
) else (
  echo [curvature] no bind URL found. Check %LOG% for details.
)

del "%LOG%" >nul 2>&1
echo.
echo [curvature] service running. Press any key to check status...
pause >nul
"%EXE%" -status
pause
