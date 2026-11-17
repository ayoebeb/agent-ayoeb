# sim-common.ps1: helpers for the daily-AO simulations. Dot-sourced by the sim-*.ps1
# scripts; not run directly.
#
# A simulation never touches the real AO: it runs in a scratch folder under %TEMP%
# with its own "AO home" (never a folder named .ao: the named pipes would collide
# with the real daemon), explicit high ports, a stand-in for the Windows
# Credential Manager (Git Credential Manager's plaintext store inside the scratch
# folder), and its own HKCU subkeys instead of the real Run key / user PATH.
#Requires -Version 7.4
$script:SimPass = 0
$script:SimFail = 0

function Assert-Sim([bool]$Condition, [string]$Message) {
	if ($Condition) { Write-Host "  PASS  $Message" -ForegroundColor Green; $script:SimPass++ }
	else { Write-Host "  FAIL  $Message" -ForegroundColor Red; $script:SimFail++ }
}

function Write-SimStep([string]$Message) { Write-Host "`n== $Message" -ForegroundColor Cyan }

function New-SimRoot([string]$Name) {
	$root = Join-Path ([IO.Path]::GetTempPath()) "ao-sim-$Name"
	if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
	New-Item -ItemType Directory -Path $root | Out-Null
	return $root
}

# Writes the JSON config the scripts take with -ConfigFile and returns its path.
function New-SimConfig([string]$Root, [int]$Port, [string]$RepoUrl, [hashtable]$Extra = @{}) {
	foreach ($d in 'shortcuts', 'startup') { New-Item -ItemType Directory -Force -Path (Join-Path $Root $d) | Out-Null }
	$c = [ordered]@{
		simulation   = $true
		aoHome       = Join-Path $Root 'aohome'
		port         = $Port
		installedDir = Join-Path $Root 'installed-app'
		shortcutDirs = @(Join-Path $Root 'shortcuts')
		startupDir   = Join-Path $Root 'startup'
		runKey       = 'HKCU:\Software\AoDailySim\Run'
		taskFolder   = '\AoDailySim'
		envRegKey    = 'Software\AoDailySim\Environment'
		repoUrl      = $RepoUrl
		fnmRoot      = $null
	}
	foreach ($k in $Extra.Keys) { $c[$k] = $Extra[$k] }
	$file = Join-Path $Root 'sim-config.json'
	$c | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $file -Encoding utf8
	return $file
}

# Searches every file under Root (except folders named in Skip) for any of the
# strings; returns the files that contain one. Used to prove a secret never
# reached a log, config or state file.
function Find-SecretLeaks([string]$Root, [string[]]$Secrets, [string[]]$SkipDirs = @(), [string[]]$SkipFiles = @()) {
	foreach ($f in Get-ChildItem -LiteralPath $Root -Recurse -File -Force -ErrorAction SilentlyContinue) {
		if ($SkipDirs | Where-Object { $f.FullName -like "*\$_\*" }) { continue }
		if ($SkipFiles -contains $f.Name) { continue }
		if ($f.Length -gt 50MB) { continue }
		# Open with sharing: a running server may hold its log open.
		try {
			$fs = [IO.File]::Open($f.FullName, 'Open', 'Read', 'ReadWrite')
			try { $ms = [IO.MemoryStream]::new(); $fs.CopyTo($ms); $bytes = $ms.ToArray() } finally { $fs.Dispose() }
		} catch { continue }
		$text = [Text.Encoding]::Latin1.GetString($bytes)
		foreach ($s in $Secrets) { if ($text.Contains($s)) { $f.FullName; break } }
	}
}

function Write-SimSummary {
	Write-Host ''
	$color = if ($script:SimFail -eq 0) { 'Green' } else { 'Red' }
	Write-Host "RESULT: $($script:SimPass) passed, $($script:SimFail) failed" -ForegroundColor $color
	return $script:SimFail
}
