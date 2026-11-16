<#
.SYNOPSIS
  Runs bootstrap-pwsh.ps1 in Windows PowerShell 5.1 on a machine without `pwsh`
  (pwsh removed from the PATH), into a scratch AO home. Downloads the real
  PowerShell zip from GitHub (about 110 MB) and checks the checksum logic for real.
  Touches nothing outside %TEMP%\ao-sim-bootstrap.

  Run:  pwsh -NoProfile -File scripts\daily-ao\tests\sim-bootstrap-pwsh.ps1
#>
[CmdletBinding()]
param([switch]$Keep)
$ErrorActionPreference = 'Stop'
$scriptsDir = Split-Path $PSScriptRoot -Parent
. (Join-Path $PSScriptRoot 'sim-common.ps1')
$root = New-SimRoot 'bootstrap'
$aoHome = Join-Path $root 'aohome'
$savedPath = $env:Path
try {
	$env:Path = (($savedPath -split ';') | Where-Object { $_ -and -not (Test-Path (Join-Path $_ 'pwsh.exe')) }) -join ';'
	Write-SimStep '1. No pwsh on the PATH: Windows PowerShell 5.1 installs a portable one'
	Assert-Sim (-not (Get-Command pwsh -ErrorAction SilentlyContinue)) 'pwsh is not on the PATH'
	$out = & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $scriptsDir 'bootstrap-pwsh.ps1') -AoHome $aoHome 2>&1 | Out-String
	Assert-Sim ($LASTEXITCODE -eq 0) 'bootstrap-pwsh.ps1 succeeded in Windows PowerShell 5.1'
	if ($LASTEXITCODE -ne 0) { Write-Host $out }
	$exe = Get-ChildItem -LiteralPath (Join-Path $aoHome 'tools') -Directory -Filter 'pwsh-*' -ErrorAction SilentlyContinue | ForEach-Object { Join-Path $_.FullName 'pwsh.exe' } | Select-Object -First 1
	Assert-Sim ([bool]$exe -and (Test-Path $exe)) "pwsh.exe installed under tools ($exe)"
	if ($exe) {
		$v = [version](([string](& $exe -NoProfile -Command '$PSVersionTable.PSVersion.ToString()')).Split('-')[0])
		Assert-Sim ($v -ge [version]'7.4') "it runs and is PowerShell $v (7.4 or newer)"
	}
	Assert-Sim ($out -match 'checksum gecontroleerd') 'the output says the checksum was verified'
	Assert-Sim (-not (Get-ChildItem -LiteralPath (Join-Path $aoHome 'tools') -Force -ErrorAction SilentlyContinue | Where-Object { $_.Name -match '\.(zip|part|partial)$' })) 'no archive or partial folder left behind'
	Assert-Sim ($out -match 'start-daily-ao\.ps1" -Prepare') 'it prints the exact next command'
	Write-SimStep '2. Run again: it recognises the portable copy'
	$again = & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $scriptsDir 'bootstrap-pwsh.ps1') -AoHome $aoHome 2>&1 | Out-String
	Assert-Sim ($again -match 'al aanwezig') 'second run: PowerShell 7 is al aanwezig (no second download)'
} finally {
	$env:Path = $savedPath
	if (-not $Keep) { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}
$fails = Write-SimSummary
exit $(if ($fails -eq 0) { 0 } else { 1 })
