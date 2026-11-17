<#
.SYNOPSIS
  Simulates a FRESH second PC: Git and PowerShell 7 exist, but Node and Go do not
  (not on the PATH, no fnm). Runs `start-daily-ao.ps1 -Prepare` from this working
  tree against a local bare copy of origin/main, with its own AO home, its own npm
  and Electron caches and a high port, and checks that portable Node and Go (and
  PowerShell) were installed under <AO home>\tools with verified checksums, that the
  build ran with them, and that nothing outside the scratch folder changed (the real
  ~/.ao scripts and tools, the real user PATH).

  Takes 15 to 30 minutes (Windows Defender makes unpacking slow) and needs about 4 GB in %TEMP%: it downloads Node, Go and
  PowerShell for real (that is the point) and runs npm ci and the Go build.

  Run:  pwsh -NoProfile -File scripts\daily-ao\tests\sim-fresh-machine.ps1
#>
[CmdletBinding()]
param([int]$Port = 3691, [switch]$Keep)
$ErrorActionPreference = 'Stop'
$scriptsDir = Split-Path $PSScriptRoot -Parent
. (Join-Path $PSScriptRoot 'sim-common.ps1')

$repoRoot = ([string](& git -C $scriptsDir rev-parse --show-toplevel)).Trim()
$root = New-SimRoot 'fresh'
$realAo = Join-Path ([Environment]::GetFolderPath('UserProfile')) '.ao'
$log = Join-Path $root 'prepare.log'

function Get-RealUserPath { [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment').GetValue('Path', '', 'DoNotExpandEnvironmentNames') }
function Get-DirHashes([string]$Dir) {
	if (-not (Test-Path -LiteralPath $Dir)) { return '(absent)' }
	(Get-ChildItem -LiteralPath $Dir -File -ErrorAction SilentlyContinue | Sort-Object Name | ForEach-Object { "$($_.Name):$((Get-FileHash $_.FullName -Algorithm SHA256).Hash)" }) -join ';'
}
function Get-DirNames([string]$Dir) { if (Test-Path -LiteralPath $Dir) { (Get-ChildItem -LiteralPath $Dir -Force -ErrorAction SilentlyContinue | Sort-Object Name | ForEach-Object Name) -join ';' } else { '(absent)' } }

$savedEnv = @{}
foreach ($e in Get-ChildItem Env:) { $savedEnv[$e.Name] = $e.Value }
try {
	Write-Host "Scratch folder: $root"
	$before = @{ Path = Get-RealUserPath; Scripts = Get-DirHashes (Join-Path $realAo 'daily\scripts'); Tools = Get-DirNames (Join-Path $realAo 'tools') }

	# --- A local stand-in for GitHub: a bare copy of origin/main -------------------------
	$bare = Join-Path $root 'origin.git'
	git init -q --bare $bare
	git -C $bare fetch -q $repoRoot 'refs/remotes/origin/main:refs/heads/main'
	git -C $bare symbolic-ref HEAD refs/heads/main
	$mainSha = ([string](& git -C $bare rev-parse main)).Trim()
	Write-Host "origin/main = $mainSha"
	$cfgFile = New-SimConfig $root $Port $bare

	# --- The fresh machine: no Node, no Go, no fnm, nothing AO-related in the environment ---
	$keepDir = { param($d) $d -and -not ((Test-Path (Join-Path $d 'node.exe')) -or (Test-Path (Join-Path $d 'go.exe')) -or (Test-Path (Join-Path $d 'npm.cmd')) -or (Test-Path (Join-Path $d 'node.cmd')) -or $d -like '*fnm_multishells*' -or $d -like '*fnm*' -or $d -like '*\nodejs*') }
	$env:Path = (($savedEnv['Path'] -split ';') | Where-Object { & $keepDir $_ }) -join ';'
	foreach ($e in Get-ChildItem Env:) { if ($e.Name -match '^(AO_|VITE_|ELECTRON|GO|NODE|npm_|NPM_|FNM_)') { Remove-Item "Env:$($e.Name)" } }
	$env:npm_config_cache = Join-Path $root 'npm-cache'
	$env:ELECTRON_CACHE = Join-Path $root 'electron-cache'
	Write-SimStep '0. The machine really has no Node or Go'
	$probe = & pwsh -NoProfile -Command 'Get-Command node, go, npm, fnm -ErrorAction SilentlyContinue | ForEach-Object Source'
	Assert-Sim (-not $probe) "node, go, npm and fnm are all absent from the PATH ($probe)"
	Assert-Sim ([bool](Get-Command git -ErrorAction SilentlyContinue)) 'Git is present'

	Write-SimStep '1. start-daily-ao.ps1 -Prepare on the fresh machine (this takes a while)'
	$started = Get-Date
	& pwsh -NoProfile -ExecutionPolicy Bypass -File (Join-Path $scriptsDir 'start-daily-ao.ps1') -Prepare -ConfigFile $cfgFile *> $log
	$code = $LASTEXITCODE
	Write-Host ("    finished in {0:n1} minutes, exit code {1}" -f ((Get-Date) - $started).TotalMinutes, $code)
	$text = if (Test-Path $log) { Get-Content -Raw $log } else { '' }
	Assert-Sim ($code -eq 0) '-Prepare succeeded'
	if ($code -ne 0) { Write-Host ($text -split "`n" | Select-Object -Last 30 | Out-String) }

	$aoHome = Join-Path $root 'aohome'
	$tools = Join-Path $aoHome 'tools'
	Write-SimStep '2. What it installed, and how'
	$node = Get-ChildItem -LiteralPath $tools -Directory -Filter 'node-v24.*-win-x64' -ErrorAction SilentlyContinue | Select-Object -First 1
	Assert-Sim ([bool]$node -and (Test-Path (Join-Path $node.FullName 'node.exe'))) "portable Node 24 installed: $($node.Name)"
	if ($node) { Assert-Sim (([string](& (Join-Path $node.FullName 'node.exe') -v)).Trim() -eq ($node.Name -replace '^node-|-win-x64$', '')) 'its node.exe reports the version in its folder name' }
	Assert-Sim ($text -match 'Node 24 installeren') 'the log shows the Node install step'
	Assert-Sim ($text -notmatch 'Checksum komt niet overeen') 'no checksum complaint'
	$goVer = (Get-Content (Join-Path $repoRoot 'backend\go.mod') | Where-Object { $_ -match '^go \d' } | Select-Object -First 1) -replace '^go ', ''
	$goExe = Get-ChildItem -LiteralPath $tools -Recurse -Filter go.exe -Depth 3 -ErrorAction SilentlyContinue | Where-Object { $_.FullName -like '*\go-*\go\bin\go.exe' } | Select-Object -First 1
	Assert-Sim ([bool]$goExe) "portable Go installed ($($goExe.FullName.Substring($root.Length)); go.mod wants $goVer)"
	Assert-Sim ($text -match 'Go [\d.]+ installeren') 'the log shows the Go install step (go.dev checksum verified before unpacking)'
	$pw = Get-ChildItem -LiteralPath $tools -Directory -Filter 'pwsh-*' -ErrorAction SilentlyContinue | Select-Object -First 1
	Assert-Sim ([bool]$pw -and (Test-Path (Join-Path $pw.FullName 'pwsh.exe'))) "portable PowerShell for the boot task installed: $($pw.Name)"
	Assert-Sim (-not (Get-ChildItem -LiteralPath $tools -File -Filter '*.zip*' -ErrorAction SilentlyContinue)) 'no downloaded archive left behind in tools'

	Write-SimStep '3. The build ran with those tools'
	$checkout = Join-Path $aoHome 'daily\agent-ayoeb'
	Assert-Sim ((([string](& git -C $checkout rev-parse HEAD)).Trim()) -eq $mainSha) 'the checkout is on origin/main'
	Assert-Sim (Test-Path (Join-Path $checkout 'frontend\node_modules\electron\dist\electron.exe')) 'npm ci ran and Electron is installed'
	$manifest = Join-Path $checkout 'frontend\daemon\dev-daemon.json'
	Assert-Sim (Test-Path $manifest) 'the Go daemon was built (dev-daemon.json)'
	$ao = Join-Path $aoHome 'daily\bin\ao.exe'
	Assert-Sim (Test-Path $ao) 'the stable ao.exe copy exists'
	if (Test-Path $ao) { $ver = ([string](@(& $ao version 2>&1)[0])); Assert-Sim ($ver -match '\S') "ao.exe runs: $ver" }
	foreach ($n in 'start-daily-ao.ps1', 'update-daily-ao.ps1', 'rollback-to-installed-ao.ps1', 'daily-ao-common.ps1', 'daily-ao-tools.ps1', 'daily-ao-github.ps1', 'daily-ao-task-helper.ps1') {
		Assert-Sim (Test-Path (Join-Path $aoHome "daily\scripts\$n")) "installed script: $n"
	}
	$state = Get-Content -Raw (Join-Path $aoHome 'daily\state.json') | ConvertFrom-Json
	Assert-Sim ($state.preparedCommit -eq $mainSha) 'state.json records the prepared commit'

	Write-SimStep '4. The read-only switch check on the prepared machine'
	$check = & pwsh -NoProfile -ExecutionPolicy Bypass -File (Join-Path $aoHome 'daily\scripts\start-daily-ao.ps1') -SwitchFromInstalled -CheckOnly -ConfigFile $cfgFile *>&1 | Out-String
	$checkCode = $LASTEXITCODE
	Assert-Sim ($checkCode -eq 0) "-SwitchFromInstalled -CheckOnly exits 0 ($checkCode)"
	Assert-Sim ($check -match 'Git version' -and $check -match 'Node v24') 'the check names Git and the portable Node'
	Assert-Sim ($check -match 'Resultaat: alles OK') 'the check says the switch can go ahead'
	if ($checkCode -ne 0) { Write-Host $check }

	Write-SimStep '5. Nothing outside the scratch folder changed'
	Assert-Sim ((Get-RealUserPath) -ceq $before.Path) 'the real user PATH is byte-for-byte unchanged'
	Assert-Sim ((Get-DirHashes (Join-Path $realAo 'daily\scripts')) -ceq $before.Scripts) "the real ~/.ao/daily/scripts files are unchanged"
	Assert-Sim ((Get-DirNames (Join-Path $realAo 'tools')) -ceq $before.Tools) 'the real ~/.ao/tools has no new entries'
	Assert-Sim (-not (Test-Path 'HKCU:\Software\AoDailySim\Environment')) 'the simulated user environment key was not even created (Prepare does not touch the PATH)'
} finally {
	foreach ($e in Get-ChildItem Env:) { if (-not $savedEnv.ContainsKey($e.Name)) { Remove-Item "Env:$($e.Name)" -ErrorAction SilentlyContinue } }
	foreach ($k in $savedEnv.Keys) { [Environment]::SetEnvironmentVariable($k, $savedEnv[$k]) }
	Remove-Item 'HKCU:\Software\AoDailySim' -Recurse -Force -ErrorAction SilentlyContinue
	if ($Keep) { Write-Host "Kept: $root" } else { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}
$fails = Write-SimSummary
exit $(if ($fails -eq 0) { 0 } else { 1 })
