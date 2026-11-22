<#
.SYNOPSIS
  Unit-level simulations of the daily-AO helpers: checksum refusal, Node detection
  order, Git-missing message, script sync, detection instead of hard-coded paths.
  Touches nothing outside %TEMP%\ao-sim-unit. No network except a listener on a
  high local port.

  Run:  pwsh -NoProfile -File scripts\daily-ao\tests\sim-unit.ps1
#>
[CmdletBinding()]
param([int]$Port = 3682, [switch]$Keep)
$ErrorActionPreference = 'Stop'
$scriptsDir = Split-Path $PSScriptRoot -Parent
. (Join-Path $PSScriptRoot 'sim-common.ps1')
$root = New-SimRoot 'unit'
$savedEnv = @{}
foreach ($k in 'PATH', 'ProgramFiles', 'ProgramFiles(x86)', 'LOCALAPPDATA') { $savedEnv[$k] = [Environment]::GetEnvironmentVariable($k) }

try {
	$cfgFile = New-SimConfig $root $Port 'http://127.0.0.1:1/ayoebeb/agent-ayoeb.git'
	. (Join-Path $scriptsDir 'daily-ao-common.ps1')
	$cfg = Get-DailyConfig -ConfigFile $cfgFile
	Initialize-DailyDirs $cfg

	Write-SimStep '1. Downloads are checked against the published SHA-256'
	$payload = [Text.Encoding]::UTF8.GetBytes('simulated toolchain archive')
	$good = (Get-FileHash -InputStream ([IO.MemoryStream]::new($payload)) -Algorithm SHA256).Hash
	$listener = [Net.HttpListener]::new(); $listener.Prefixes.Add("http://127.0.0.1:$Port/"); $listener.Start()
	$serve = [powershell]::Create().AddScript({ param($l, $b) while ($l.IsListening) { try { $c = $l.GetContext(); $c.Response.OutputStream.Write($b, 0, $b.Length); $c.Response.Close() } catch { break } } }).AddArgument($listener).AddArgument($payload)
	$job = $serve.BeginInvoke()
	try {
		$out = Join-Path $root 'archive.zip'
		Get-VerifiedDownload -Uri "http://127.0.0.1:$Port/archive.zip" -OutFile $out -Sha256 $good
		Assert-Sim ((Test-Path $out) -and (Get-FileSha256 $out) -eq $good) 'a download with the right checksum is kept'
		Remove-Item $out
		$threw = $false; $msg = ''
		try { Get-VerifiedDownload -Uri "http://127.0.0.1:$Port/archive.zip" -OutFile $out -Sha256 ('0' * 64) } catch { $threw = $true; $msg = "$_" }
		Assert-Sim ($threw -and $msg -match 'Checksum komt niet overeen') 'a download with the wrong checksum is refused'
		Assert-Sim (-not (Test-Path $out) -and -not (Test-Path "$out.part")) 'and nothing unverified is left behind'
		$threw = $false
		try { Get-VerifiedDownload -Uri "http://127.0.0.1:$Port/archive.zip" -OutFile $out -Sha256 'not-a-hash' } catch { $threw = $true }
		Assert-Sim $threw 'a missing or malformed checksum is refused before anything is downloaded'
	} finally { $listener.Stop(); $listener.Close(); $serve.Dispose() }

	Write-SimStep '2. Git failure kinds and the dry-run push verdict'
	$kinds = @(
		@('fatal: Authentication failed for ''https://github.com/x/y.git/''', 'auth'),
		@('fatal: could not read Username for ''https://github.com'': terminal prompts disabled', 'auth'),
		@('fatal: Cannot prompt because user interactivity has been disabled.', 'auth'),
		@('remote: Repository not found.', 'auth'),
		@('fatal: repository ''https://github.com/x/y.git/'' not found', 'auth'),
		@('fatal: unable to access ''https://github.com/x/y.git/'': Could not resolve host: github.com', 'network'),
		@('fatal: unable to access ''https://github.com/x/y.git/'': Failed to connect to github.com port 443', 'network'),
		@('fatal: unable to access ''https://x/'': The requested URL returned error: 403', 'auth'),
		@('error: pathspec ''zz'' did not match any file(s) known to git', 'other'))
	foreach ($k in $kinds) { Assert-Sim ((Get-GitFailureKind $k[0]) -eq $k[1]) "'$($k[0].Substring(0, [Math]::Min(60, $k[0].Length)))' is $($k[1])" }
	Assert-Sim ((Get-WriteProbeVerdict 0 "To x`n*`tHEAD:refs/heads/p`t[new branch]") -eq 'write-capable') 'a dry-run push that would succeed = write-capable'
	Assert-Sim ((Get-WriteProbeVerdict 128 'fatal: unable to access ''x'': The requested URL returned error: 403') -eq 'read-only') 'a 403 on push = read-only'
	Assert-Sim ((Get-WriteProbeVerdict 128 'remote: Permission to a/b.git denied to c.') -eq 'read-only') '"Permission denied" on push = read-only'
	Assert-Sim ((Get-WriteProbeVerdict 128 'fatal: Authentication failed for ''x''') -eq 'invalid') 'a refused login = invalid'
	Assert-Sim ((Get-WriteProbeVerdict 1 'something else') -eq 'unknown') 'anything else = unknown'

	Write-SimStep '3. Node: fnm, then portable, then PATH; another major is a last resort'
	$fnm = Join-Path $root 'fnm\node-versions'
	New-Item -ItemType Directory -Force -Path (Join-Path $fnm 'v24.3.0\installation'), (Join-Path $fnm 'v24.11.1\installation'), (Join-Path $fnm 'v22.9.0\installation') | Out-Null
	foreach ($v in 'v24.3.0', 'v24.11.1', 'v22.9.0') { Set-Content -LiteralPath (Join-Path $fnm "$v\installation\node.exe") -Value 'x' }
	$cfgFnm = Get-DailyConfig -ConfigFile (New-SimConfig $root $Port 'http://127.0.0.1:1/a/b.git' @{ fnmRoot = $fnm })
	$c = Get-NodeCandidate $cfgFnm
	Assert-Sim ($c -and $c.Source -eq 'fnm' -and $c.Exact -and $c.Dir -like '*v24.11.1*') 'fnm: the newest Node 24 wins (v24.11.1 over v24.3.0 and 22)'
	New-Item -ItemType Directory -Force -Path (Join-Path $cfg.ToolsDir 'node-v24.2.0-win-x64'), (Join-Path $cfg.ToolsDir 'node-v24.10.0-win-x64'), (Join-Path $cfg.ToolsDir 'node-v22.1.0-win-x64') | Out-Null
	foreach ($d in 'node-v24.2.0-win-x64', 'node-v24.10.0-win-x64', 'node-v22.1.0-win-x64') { Set-Content -LiteralPath (Join-Path $cfg.ToolsDir "$d\node.exe") -Value 'x' }
	$c = Get-NodeCandidate $cfg
	Assert-Sim ($c -and $c.Source -eq 'portable' -and $c.Exact -and $c.Dir -like '*node-v24.10.0-win-x64') 'portable: the newest Node 24 under tools wins (numeric order, 24.10.0 over 24.2.0)'
	Remove-Item (Join-Path $cfg.ToolsDir 'node-v2*') -Recurse -Force
	function Set-FakeNode([string]$Version) {
		$dir = Join-Path $root "pathnode-$Version"; New-Item -ItemType Directory -Force -Path $dir | Out-Null
		Set-Content -LiteralPath (Join-Path $dir 'node.cmd') -Value "@echo v$Version"
		[Environment]::SetEnvironmentVariable('PATH', "$dir;" + ($savedEnv['PATH'] -split ';' | Where-Object { $_ -and -not (Test-Path (Join-Path $_ 'node.exe')) -and -not (Test-Path (Join-Path $_ 'node.cmd')) } | ForEach-Object { $_ }) -join ';')
	}
	Set-FakeNode '24.9.9'
	$c = Get-NodeCandidate $cfg
	Assert-Sim ($c -and $c.Source -eq 'path' -and $c.Exact) 'PATH: Node 24 on the PATH is accepted'
	Set-FakeNode '22.5.0'
	$c = Get-NodeCandidate $cfg
	Assert-Sim ($c -and $c.Source -eq 'path' -and -not $c.Exact) 'PATH: Node 22 is accepted only as a last resort (not exact, so a portable 24 gets installed)'
	Set-FakeNode '20.1.0'
	Assert-Sim ($null -eq (Get-NodeCandidate $cfg)) 'PATH: Node 20 is too old: no candidate (a portable Node 24 gets installed)'
	$threw = $false; $msg = ''
	try { Get-NodeDir $cfg | Out-Null } catch { $threw = $true; $msg = "$_" }
	Assert-Sim ($threw -and $msg -match '-Prepare installeert') 'Get-NodeDir (never installs) says what to do'
	[Environment]::SetEnvironmentVariable('PATH', ($savedEnv['PATH'] -split ';' | Where-Object { $_ -and -not (Test-Path (Join-Path $_ 'node.exe')) -and -not (Test-Path (Join-Path $_ 'node.cmd')) }) -join ';')
	Assert-Sim ($null -eq (Get-NodeCandidate $cfg)) 'no Node anywhere: no candidate'
	$real = Join-Path $env:APPDATA 'fnm\node-versions'
	if (Test-Path -LiteralPath $real) {
		$cfgReal = Get-DailyConfig -ConfigFile (New-SimConfig $root $Port 'http://127.0.0.1:1/a/b.git' @{ fnmRoot = $real })
		$c = Get-NodeCandidate $cfgReal
		Assert-Sim ($c -and $c.Source -eq 'fnm' -and $c.Exact) "this PC's fnm Node 24 is still found first ($($c.Dir)): behaviour unchanged here"
	}
	[Environment]::SetEnvironmentVariable('PATH', $savedEnv['PATH'])

	Write-SimStep '4. Git missing: a clear message'
	$noGit = ($savedEnv['PATH'] -split ';' | Where-Object { $_ -and -not (Test-Path (Join-Path $_ 'git.exe')) }) -join ';'
	[Environment]::SetEnvironmentVariable('PATH', $noGit)
	foreach ($k in 'ProgramFiles', 'ProgramFiles(x86)', 'LOCALAPPDATA') { [Environment]::SetEnvironmentVariable($k, (Join-Path $root 'nowhere')) }
	$threw = $false; $msg = ''
	try { Assert-GitAvailable } catch { $threw = $true; $msg = "$_" }
	Assert-Sim ($threw -and $msg -match 'Git ontbreekt' -and $msg -match 'winget install --id Git.Git' -and $msg -match 'git-scm.com') 'Assert-GitAvailable explains how to install Git'
	foreach ($k in 'PATH', 'ProgramFiles', 'ProgramFiles(x86)', 'LOCALAPPDATA') { [Environment]::SetEnvironmentVariable($k, $savedEnv[$k]) }
	Assert-Sim ((Add-GitToPathIfInstalled)) 'with Git present it passes'

	Write-SimStep '5. Scripts: install copies, sync from checkout refuses a broken commit'
	$src = Join-Path $root 'clone\scripts\daily-ao'
	New-Item -ItemType Directory -Force -Path $src, (Join-Path $src 'tests') | Out-Null
	Get-ChildItem -LiteralPath $scriptsDir -Filter '*.ps1' | Copy-Item -Destination $src
	Copy-Item (Join-Path $PSScriptRoot 'sim-common.ps1') (Join-Path $src 'tests')
	Install-DailyScripts $cfg $src
	$installed = @(Get-ChildItem -LiteralPath $cfg.ScriptsDir -File | ForEach-Object Name)
	Assert-Sim ($installed -contains 'start-daily-ao.ps1' -and $installed -contains 'daily-ao-github.ps1' -and $installed -contains 'daily-ao-tools.ps1') 'Install-DailyScripts copies every script, including the new modules'
	Assert-Sim ($installed -notcontains 'bootstrap-pwsh.ps1' -and $installed -notcontains 'sim-common.ps1') 'but not bootstrap-pwsh.ps1 or the tests'
	$cfg2 = Get-DailyConfig -ConfigFile (New-SimConfig $root $Port 'http://127.0.0.1:1/a/b.git' @{ checkout = (Join-Path $root 'clone') })
	Add-Content -LiteralPath (Join-Path $src 'update-daily-ao.ps1') -Value "`n# changed on main"
	Sync-DailyScriptsFromCheckout $cfg2
	Assert-Sim ((Get-Content -Raw (Join-Path $cfg.ScriptsDir 'update-daily-ao.ps1')) -match 'changed on main') 'a changed script on main reaches the installed copy'
	Add-Content -LiteralPath (Join-Path $src 'start-daily-ao.ps1') -Value "`nfunction Broken( {"
	Add-Content -LiteralPath (Join-Path $src 'update-daily-ao.ps1') -Value "`n# second change"
	Sync-DailyScriptsFromCheckout $cfg2
	Assert-Sim ((Get-Content -Raw (Join-Path $cfg.ScriptsDir 'start-daily-ao.ps1')) -cnotmatch 'function Broken') 'a script with a syntax error is NOT copied'
	Assert-Sim ((Get-Content -Raw (Join-Path $cfg.ScriptsDir 'update-daily-ao.ps1')) -notmatch 'second change') '...and then nothing is copied at all (the set stays consistent)'

	Write-SimStep '6. Detection instead of one PC''s paths'
	Assert-Sim ($null -eq $cfg.ReferenceRepo) 'no default reference repository (it was a path on one PC)'
	$fakeLocal = Join-Path $root 'localappdata'; $fakeApp = Join-Path $fakeLocal 'Programs\agent-orchestrator'
	New-Item -ItemType Directory -Force -Path $fakeApp | Out-Null; Set-Content (Join-Path $fakeApp 'agent-orchestrator.exe') 'x'
	[Environment]::SetEnvironmentVariable('LOCALAPPDATA', $fakeLocal)
	Assert-Sim ((Find-InstalledAppDir) -eq $fakeApp) 'the installed app is found in the per-user install folder'
	[Environment]::SetEnvironmentVariable('LOCALAPPDATA', $savedEnv['LOCALAPPDATA'])
	$pathNoPwsh = ($savedEnv['PATH'] -split ';' | Where-Object { $_ -and -not (Test-Path (Join-Path $_ 'pwsh.exe')) }) -join ';'
	[Environment]::SetEnvironmentVariable('PATH', $pathNoPwsh)
	$portable = Join-Path $cfg.ToolsDir 'pwsh-7.9.9'; New-Item -ItemType Directory -Force -Path $portable | Out-Null; Set-Content (Join-Path $portable 'pwsh.exe') 'x'
	Assert-Sim ((Get-PwshPath $cfg) -eq (Join-Path $portable 'pwsh.exe')) 'without pwsh on the PATH, the portable PowerShell is used'
	Remove-Item $portable -Recurse -Force
	Assert-Sim ((Get-PwshPath $cfg) -eq (Join-Path $PSHOME 'pwsh.exe')) 'and without that, the PowerShell running the script'
	[Environment]::SetEnvironmentVariable('PATH', $savedEnv['PATH'])
	$hits = @()
	foreach ($f in Get-ChildItem -LiteralPath $scriptsDir -Recurse -File -Include '*.ps1', '*.md', '*.mjs') {
		if ($f.Name -eq 'sim-unit.ps1') { continue }   # holds the patterns themselves
		$text = Get-Content -Raw -LiteralPath $f.FullName
		foreach ($p in 'C:\\Users\\Ayoeb', 'NLSTATION', '\bDevelopment\b', 'Documents\\GitHub') { if ($text -match $p) { $hits += "$($f.Name): $p" } }
	}
	Assert-Sim ($hits.Count -eq 0) "no PC-specific user name, host name, task name or path in scripts or docs ($($hits -join '; '))"
} finally {
	foreach ($k in $savedEnv.Keys) { [Environment]::SetEnvironmentVariable($k, $savedEnv[$k]) }
	if (-not $Keep) { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}
$fails = Write-SimSummary
exit $(if ($fails -eq 0) { 0 } else { 1 })
