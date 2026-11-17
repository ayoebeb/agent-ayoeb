# daily-ao-common.ps1
# Shared helpers for start-daily-ao.ps1, update-daily-ao.ps1 and
# rollback-to-installed-ao.ps1. Dot-sourced by those scripts; not run directly.
#
# The daily AO is this repository's origin/main, run as a dev Electron build that
# acts as the PRIMARY AO: the real data dir, run file, port and Electron profile.
#
# The scripts live in this repository (scripts/daily-ao). `start-daily-ao.ps1
# -Prepare` copies them to <AO home>\daily\scripts, where shortcuts and tasks point;
# update-daily-ao.ps1 keeps that copy in step with the checkout.
#Requires -Version 7.4
Set-StrictMode -Version 3.0
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$script:DailyDefaults = [ordered]@{
	RepoUrl           = 'https://github.com/ayoebeb/agent-ayoeb.git'
	Branch            = 'main'
	Port              = 3001
	NodeMajor         = 24
	# The installed release signs in with the production WorkOS client; a dev
	# build defaults to staging. Same client = the existing sign-in keeps working.
	WorkOsClientId    = 'client_01KZ3VRKTDKBZ3JA8AMKTF6W2X'
	# A dev build only claims ao-app:// (sign-in return, billing links) when it
	# signs in through a deep-link redirect; this is the production bounce page.
	CloudAuthRedirect = 'https://orchestrator.inc/app/auth/return/'
}

# Exit code update-daily-ao.ps1 uses for "a restart is waiting for your OK".
$script:ExitRestartPending = 10

function Write-Step([string]$Message) { Write-Host "==> $Message" -ForegroundColor Cyan }
function Write-Ok([string]$Message) { Write-Host "    OK  $Message" -ForegroundColor Green }
function Write-Note([string]$Message) { Write-Host "    $Message" }
function Write-Warn2([string]$Message) { Write-Host "    LET OP  $Message" -ForegroundColor Yellow }

# Toolchain (Git, Node) and GitHub-token helpers live in their own files.
. (Join-Path $PSScriptRoot 'daily-ao-tools.ps1')
. (Join-Path $PSScriptRoot 'daily-ao-github.ps1')

# The PowerShell that shortcuts and tasks start. Normally `pwsh` on the PATH; on a
# machine that only has the portable copy (under <AO home>\tools) that one, and
# as a last resort the PowerShell running this script.
function Get-PwshPath($cfg) {
	$cmd = Get-Command pwsh -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
	if ($cmd) { return $cmd.Source }
	$portable = Get-BootPwsh $cfg
	if ($portable) { return $portable }
	return (Join-Path $PSHOME 'pwsh.exe')
}

function Get-RealProfileDir { [Environment]::GetFolderPath('UserProfile') }

# Both compare paths found on the machine (task actions, shortcuts, process
# list); a value that is not a valid path simply does not match.
function Test-SamePath([string]$A, [string]$B) {
	if (-not $A -or -not $B) { return $false }
	try { return [IO.Path]::GetFullPath($A).TrimEnd('\') -ieq [IO.Path]::GetFullPath($B).TrimEnd('\') } catch { return $false }
}

function Test-PathInside([string]$Child, [string]$Parent) {
	if (-not $Child -or -not $Parent) { return $false }
	try {
		$c = [IO.Path]::GetFullPath($Child)
		$p = [IO.Path]::GetFullPath($Parent).TrimEnd('\') + '\'
		return $c.StartsWith($p, [StringComparison]::OrdinalIgnoreCase)
	} catch { return $false }
}

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
# Defaults describe the real machine, found by detection (nothing is tied to one
# PC). A JSON config file (used by the simulation) can override: aoHome, port,
# installedDir, shortcutDirs, repoUrl, referenceRepo (a local clone to borrow git
# objects from; none by default), fnmRoot (where fnm keeps Node; null = ignore
# fnm), cloudAuthRedirect (null = never claim ao-app://), extraEnv, goZipCache,
# nodeZipCache, simulation, envRegKey (simulation only: the HKCU subkey that
# stands in for Environment, so a simulation never touches the real user PATH).
function Get-DailyConfig {
	param([string]$ConfigFile)
	$o = @{}
	if ($ConfigFile) {
		$json = Get-Content -Raw -LiteralPath $ConfigFile | ConvertFrom-Json -AsHashtable
		foreach ($k in $json.Keys) { $o[$k] = $json[$k] }
	}
	$realProfile = Get-RealProfileDir
	$realAoHome = Join-Path $realProfile '.ao'
	$aoHome = if ($o['aoHome']) { [IO.Path]::GetFullPath($o['aoHome']) } else { $realAoHome }
	$isReal = Test-SamePath $aoHome $realAoHome
	$isSimulation = [bool]$o['simulation']

	# Named pipes (\\.\pipe\ao-supervise, \\.\pipe\ao-browser) are derived from
	# the run-file folder name and are global. A second AO home also named ".ao"
	# would talk to the REAL daemon's supervisor pipe.
	if (-not $isReal -and (Split-Path $aoHome -Leaf) -ieq '.ao') {
		throw "Refusing AO home '$aoHome': only the real AO home may be named .ao (pipe names would collide with the real AO)."
	}
	if ($isSimulation -and $isReal) { throw 'A simulation config must not point at the real AO home.' }

	$installedDir = if ($o['installedDir']) { $o['installedDir'] } else { Find-InstalledAppDir }
	$shortcutDirs = if ($o.ContainsKey('shortcutDirs')) { @($o['shortcutDirs']) } else {
		@((Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs'), [Environment]::GetFolderPath('Desktop'))
	}
	$dailyDir = Join-Path $aoHome 'daily'
	$checkout = if ($o['checkout']) { $o['checkout'] } else { Join-Path $dailyDir 'agent-ayoeb' }
	$toolsDir = Join-Path $aoHome 'tools'
	$extraEnv = @{}
	if ($o['extraEnv']) { foreach ($k in $o['extraEnv'].Keys) { $extraEnv[$k] = [string]$o['extraEnv'][$k] } }

	$realStartup = [Environment]::GetFolderPath('Startup')
	$realRunKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
	$startupDir = if ($o['startupDir']) { $o['startupDir'] } else { $realStartup }
	$runKey = if ($o['runKey']) { $o['runKey'] } else { $realRunKey }
	$taskFolder = if ($o['taskFolder']) { [string]$o['taskFolder'] } else { $null }
	if ($isSimulation -and ((Test-SamePath $startupDir $realStartup) -or $runKey -ieq $realRunKey -or -not $taskFolder -or $taskFolder -eq '\')) {
		throw 'A simulation config must set its own startupDir, runKey and taskFolder (never the real login items).'
	}
	# Test switches, simulation only: run the task helper without elevation,
	# start the window through the "Ayo (daily) openen" task as session 0 would,
	# and treat named tasks as boot tasks (only admins can create a task with a
	# boot trigger, so the simulation's copy has none).
	foreach ($k in 'forceTaskHelper', 'frontendViaTask', 'simBootTasks', 'envRegKey') {
		if ($o[$k] -and -not $isSimulation) { throw "Config key '$k' is only allowed in a simulation." }
	}
	# The user PATH lives in HKCU\Environment. A simulation gets its own key (a
	# path below HKCU, e.g. Software\AoDailySim\Environment); a second AO home that
	# is not a simulation never touches the real user PATH.
	$envRegKey = if ($o['envRegKey']) { [string]$o['envRegKey'] } else { 'Environment' }
	if ($isSimulation -and $envRegKey -ieq 'Environment') {
		throw 'A simulation config must set its own envRegKey (never the real user environment).'
	}
	$manageUserPath = $isReal -or [bool]$o['envRegKey']

	[pscustomobject]@{
		AoHome            = $aoHome
		IsReal            = $isReal
		IsSimulation      = $isSimulation
		RealAoHome        = $realAoHome
		DataDir           = Join-Path $aoHome 'data'
		RunFile           = Join-Path $aoHome 'running.json'
		ElectronDir       = if ($o['electronDir']) { [IO.Path]::GetFullPath($o['electronDir']) } else { Join-Path $aoHome 'electron' }
		ElectronArgs      = if ($o['electronArgs']) { @($o['electronArgs']) } else { @() }
		Port              = if ($o['port']) { [int]$o['port'] } else { $script:DailyDefaults.Port }
		DailyDir          = $dailyDir
		ScriptsDir        = Join-Path $dailyDir 'scripts'
		StateFile         = Join-Path $dailyDir 'state.json'
		LogDir            = Join-Path $dailyDir 'logs'
		BackupDir         = Join-Path $dailyDir 'backup'
		# Stable home of the daily ao.exe, on the user PATH (the daemon's own
		# folder is new on every backend rebuild).
		BinDir            = Join-Path $dailyDir 'bin'
		EnvRegKey         = $envRegKey
		ManageUserPath    = $manageUserPath
		Checkout          = $checkout
		Frontend          = Join-Path $checkout 'frontend'
		ToolsDir          = $toolsDir
		GoPath            = Join-Path $toolsDir 'gopath'
		GoCache           = Join-Path $toolsDir 'gocache'
		GoZipCache        = if ($o['goZipCache']) { $o['goZipCache'] } else { $null }
		NodeZipCache      = if ($o['nodeZipCache']) { $o['nodeZipCache'] } else { $null }
		# fnm's per-user Node installs, used when present. A config can point it
		# elsewhere or set it to null (the simulation of a machine without fnm).
		FnmRoot           = if ($o.ContainsKey('fnmRoot')) { $o['fnmRoot'] } else { Join-Path $env:APPDATA 'fnm\node-versions' }
		RepoUrl           = if ($o['repoUrl']) { $o['repoUrl'] } else { $script:DailyDefaults.RepoUrl }
		ReferenceRepo     = if ($o.ContainsKey('referenceRepo')) { $o['referenceRepo'] } else { $null }
		Branch            = $script:DailyDefaults.Branch
		InstalledDir      = $installedDir
		InstalledExe      = Join-Path $installedDir 'agent-orchestrator.exe'
		InstalledAo       = Join-Path $installedDir 'resources\daemon\ao.exe'
		ShortcutDirs      = $shortcutDirs
		StartupDir        = $startupDir
		RunKey            = $runKey
		TaskFolder        = $taskFolder
		FrontendTaskPath  = if ($taskFolder) { $taskFolder } else { '\' }
		FrontendTaskName  = 'Ayo (daily) openen'
		ForceTaskHelper   = [bool]$o['forceTaskHelper']
		FrontendViaTask   = [bool]$o['frontendViaTask']
		SimBootTasks      = if ($o['simBootTasks']) { @($o['simBootTasks']) } else { @() }
		ConfigFile        = if ($ConfigFile) { [IO.Path]::GetFullPath($ConfigFile) } else { $null }
		WorkOsClientId    = $script:DailyDefaults.WorkOsClientId
		CloudAuthRedirect = if ($o.ContainsKey('cloudAuthRedirect')) { $o['cloudAuthRedirect'] } else { $script:DailyDefaults.CloudAuthRedirect }
		ExtraEnv          = $extraEnv
	}
}

function Initialize-DailyDirs($cfg) {
	foreach ($d in @($cfg.DailyDir, $cfg.LogDir, $cfg.BackupDir, $cfg.ToolsDir)) {
		New-Item -ItemType Directory -Force -Path $d | Out-Null
	}
	# Every start writes a new Forge log; keep two weeks.
	Get-ChildItem -LiteralPath $cfg.LogDir -File -ErrorAction SilentlyContinue |
		Where-Object { $_.LastWriteTime -lt (Get-Date).AddDays(-14) } | Remove-Item -Force -ErrorAction SilentlyContinue
}

function Read-DailyState($cfg) {
	if (Test-Path -LiteralPath $cfg.StateFile) {
		return Get-Content -Raw -LiteralPath $cfg.StateFile | ConvertFrom-Json -AsHashtable
	}
	return @{}
}

function Write-DailyState($cfg, [hashtable]$State) {
	$tmp = "$($cfg.StateFile).tmp"
	$State | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $tmp -Encoding utf8
	Move-Item -LiteralPath $tmp -Destination $cfg.StateFile -Force
}

# Copy the scripts next to the daily checkout so shortcuts point at a stable path.
function Install-DailyScripts($cfg, [string]$SourceDir) {
	New-Item -ItemType Directory -Force -Path $cfg.ScriptsDir | Out-Null
	if (Test-SamePath $SourceDir $cfg.ScriptsDir) { return }
	# Every script next to this one (not the tests folder, not bootstrap-pwsh.ps1,
	# which runs before PowerShell 7 exists).
	foreach ($f in Get-ChildItem -LiteralPath $SourceDir -Filter '*.ps1' -File) {
		if ($f.Name -eq 'bootstrap-pwsh.ps1') { continue }
		Copy-Item -LiteralPath $f.FullName -Destination (Join-Path $cfg.ScriptsDir $f.Name) -Force
	}
}

# Keeps the installed copy in <AO home>\daily\scripts in step with the checkout's
# scripts/daily-ao (reviewed code on main). A copy is only replaced when every
# script in the checkout parses, so a broken commit cannot take the launcher down.
# Scripts that already run keep their old code until they start again (the
# update watcher until its next restart). Never throws: it must not fail an update.
function Sync-DailyScriptsFromCheckout($cfg) {
	try {
		$src = Join-Path $cfg.Checkout 'scripts\daily-ao'
		if (-not (Test-Path -LiteralPath $src) -or (Test-SamePath $src $cfg.ScriptsDir)) { return }
		$files = @(Get-ChildItem -LiteralPath $src -Filter '*.ps1' -File | Where-Object { $_.Name -ne 'bootstrap-pwsh.ps1' })
		$changed = @($files | Where-Object {
			$dest = Join-Path $cfg.ScriptsDir $_.Name
			-not (Test-Path -LiteralPath $dest) -or (Get-FileSha256 $dest) -ne (Get-FileSha256 $_.FullName)
		})
		if ($changed.Count -eq 0) { return }
		foreach ($f in $files) {
			$errors = $null
			[void][Management.Automation.Language.Parser]::ParseFile($f.FullName, [ref]$null, [ref]$errors)
			if ($errors -and @($errors).Count -gt 0) { Write-Warn2 "scripts in de checkout niet overgenomen: $($f.Name) heeft een syntaxfout ($(@($errors)[0].Message))"; return }
		}
		foreach ($f in $changed) { Copy-Item -LiteralPath $f.FullName -Destination (Join-Path $cfg.ScriptsDir $f.Name) -Force }
		Write-Ok "scripts bijgewerkt vanuit de checkout: $(($changed | ForEach-Object Name) -join ', ')"
	} catch { Write-Warn2 "scripts bijwerken vanuit de checkout mislukt (de update zelf gaat door): $_" }
}

# ---------------------------------------------------------------------------
# Native commands
# ---------------------------------------------------------------------------
function Invoke-Checked {
	param([string]$Exe, [string[]]$Arguments, [string]$WorkingDirectory, [string]$LogFile, [switch]$Quiet)
	$prev = Get-Location
	try {
		if ($WorkingDirectory) { Set-Location -LiteralPath $WorkingDirectory }
		if ($LogFile) {
			& $Exe @Arguments *>> $LogFile
		} elseif ($Quiet) {
			& $Exe @Arguments *> $null
		} else {
			& $Exe @Arguments
		}
		$code = $LASTEXITCODE
	} finally { Set-Location $prev }
	if ($code -ne 0) {
		$hint = if ($LogFile) { " (log: $LogFile)" } else { '' }
		throw "$([IO.Path]::GetFileName($Exe)) $($Arguments -join ' ') failed with exit code $code$hint"
	}
}

function Invoke-Git($cfg, [string[]]$Arguments) {
	# These calls run from hidden launchers and the update watcher: never wait for
	# a terminal prompt and never open a Git Credential Manager window. Set for
	# this one call only; the agents the daemon starts later must not inherit them.
	$prevPrompt = $env:GIT_TERMINAL_PROMPT; $prevInteractive = $env:GCM_INTERACTIVE
	$env:GIT_TERMINAL_PROMPT = '0'; $env:GCM_INTERACTIVE = 'never'
	try { $out = & git -C $cfg.Checkout @Arguments 2>&1; $code = $LASTEXITCODE }
	finally { $env:GIT_TERMINAL_PROMPT = $prevPrompt; $env:GCM_INTERACTIVE = $prevInteractive }
	if ($code -ne 0) {
		$hint = if (@($Arguments)[0] -in 'fetch', 'ls-remote', 'pull', 'clone' -and (Get-GitFailureKind "$out") -eq 'auth') { " ($(Get-GitAuthHint))" } else { '' }
		throw "git $($Arguments -join ' ') failed: $out$hint"
	}
	return $out
}

# ---------------------------------------------------------------------------
# Toolchains (user-level only: nothing system-wide)
# ---------------------------------------------------------------------------
function Get-RequiredGoVersion($cfg) {
	$mod = Get-Content -LiteralPath (Join-Path $cfg.Checkout 'backend\go.mod')
	$tool = $mod | Where-Object { $_ -match '^toolchain go(\d+\.\d+(\.\d+)?)' } | Select-Object -First 1
	if ($tool) { return ($tool -replace '^toolchain go', '').Trim() }
	$go = $mod | Where-Object { $_ -match '^go \d+\.\d+' } | Select-Object -First 1
	if (-not $go) { throw 'Cannot read the Go version from backend/go.mod' }
	$v = ($go -replace '^go ', '').Trim()
	# "go 1.28" means the 1.28.0 release (that is how go.dev names the download).
	if ($v -match '^\d+\.\d+$') { $v = "$v.0" }
	return $v
}

function Get-GoRoot($cfg, [string]$Version) { Join-Path $cfg.ToolsDir "go-$Version\go" }

# Installs the Go version backend/go.mod asks for under <AO home>\tools\go-<ver>.
# Source: an already downloaded official zip if one is given/found, else go.dev.
# Either way the zip's SHA-256 is checked against go.dev's published checksum.
function Install-GoToolchain($cfg) {
	$version = Get-RequiredGoVersion $cfg
	$goRoot = Get-GoRoot $cfg $version
	$goExe = Join-Path $goRoot 'bin\go.exe'
	if (Test-Path -LiteralPath $goExe) {
		$v = & $goExe version
		if ($v -match "go$([regex]::Escape($version)) ") { Write-Ok "Go $version ($goRoot)"; return $goRoot }
	}
	$file = "go$version.windows-amd64.zip"
	Write-Step "Go $version installeren in $($cfg.ToolsDir) (alleen voor AO, niet systeembreed)"
	$meta = Invoke-RestMethod -Uri 'https://go.dev/dl/?mode=json&include=all' -TimeoutSec 60
	$entry = $meta | ForEach-Object { $_.files } | Where-Object { $_.filename -eq $file } | Select-Object -First 1
	if (-not $entry) { throw "go.dev lists no $file" }
	$candidates = @($cfg.GoZipCache) | Where-Object { $_ -and (Test-Path -LiteralPath $_) }
	$zip = $null
	foreach ($c in $candidates) {
		if ((Get-FileHash -Algorithm SHA256 -LiteralPath $c).Hash -ieq $entry.sha256) { $zip = $c; Write-Note "using verified local copy $c"; break }
	}
	if (-not $zip) {
		$zip = Join-Path $cfg.ToolsDir $file
		Invoke-WebRequest -Uri "https://go.dev/dl/$file" -OutFile $zip -TimeoutSec 600
		if ((Get-FileHash -Algorithm SHA256 -LiteralPath $zip).Hash -ine $entry.sha256) {
			Remove-Item -LiteralPath $zip -Force
			throw "Checksum mismatch for $file"
		}
	}
	$staging = Join-Path $cfg.ToolsDir "go-$version.partial"
	if (Test-Path -LiteralPath $staging) { Remove-Item -LiteralPath $staging -Recurse -Force }
	Expand-Archive -LiteralPath $zip -DestinationPath $staging
	$final = Split-Path $goRoot -Parent
	if (Test-Path -LiteralPath $final) { Remove-Item -LiteralPath $final -Recurse -Force }
	Move-Item -LiteralPath $staging -Destination $final
	if (Test-SamePath (Split-Path $zip -Parent) $cfg.ToolsDir) { Remove-Item -LiteralPath $zip -Force }
	Write-Ok "Go $version ($goRoot)"
	return $goRoot
}

# PowerShell for the boot task. The installed pwsh is the Microsoft Store
# version: an app alias, which a task running before sign-in (session 0) may
# not be able to start. A portable copy of the same version under
# <AO home>\tools is a plain exe. Checked against GitHub's SHA-256 for it.
function Get-BootPwsh($cfg) {
	$dirs = @(Get-ChildItem -LiteralPath $cfg.ToolsDir -Directory -Filter 'pwsh-*' -ErrorAction SilentlyContinue |
		Where-Object { $_.Name -match '^pwsh-\d+\.\d+\.\d+$' -and (Test-Path -LiteralPath (Join-Path $_.FullName 'pwsh.exe')) } |
		Sort-Object { [version]($_.Name.Substring(5)) } -Descending)
	if (@($dirs).Count -eq 0) { return $null }
	return Join-Path $dirs[0].FullName 'pwsh.exe'
}

function Install-PortablePwsh($cfg) {
	$have = Get-BootPwsh $cfg
	if ($have) { Write-Ok "PowerShell voor de opstart-taak: $have"; return $have }
	$version = $PSVersionTable.PSVersion.ToString()
	$file = "PowerShell-$version-win-x64.zip"
	Write-Step "PowerShell $version los installeren in $($cfg.ToolsDir) (alleen voor de opstart-taak, niet systeembreed)"
	$rel = Invoke-RestMethod -Uri "https://api.github.com/repos/PowerShell/PowerShell/releases/tags/v$version" -TimeoutSec 60
	$asset = $rel.assets | Where-Object { $_.name -eq $file } | Select-Object -First 1
	if (-not $asset) { throw "PowerShell release v$version has no $file" }
	$want = $null
	if ($asset.PSObject.Properties['digest'] -and "$($asset.digest)" -match '^sha256:([0-9a-fA-F]{64})$') { $want = $Matches[1] }
	if (-not $want) {
		$h = $rel.assets | Where-Object { $_.name -eq 'hashes.sha256' } | Select-Object -First 1
		if ($h) {
			$line = (Invoke-RestMethod -Uri $h.browser_download_url -TimeoutSec 60) -split "`n" | Where-Object { $_ -match [regex]::Escape($file) } | Select-Object -First 1
			if ($line -match '([0-9a-fA-F]{64})') { $want = $Matches[1] }
		}
	}
	if (-not $want) { throw "No published SHA-256 for $file" }
	$zip = Join-Path $cfg.ToolsDir $file
	Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $zip -TimeoutSec 900
	if ((Get-FileHash -Algorithm SHA256 -LiteralPath $zip).Hash -ine $want) { Remove-Item -LiteralPath $zip -Force; throw "Checksum mismatch for $file" }
	$final = Join-Path $cfg.ToolsDir "pwsh-$version"
	$staging = "$final.partial"
	if (Test-Path -LiteralPath $staging) { Remove-Item -LiteralPath $staging -Recurse -Force }
	Expand-Archive -LiteralPath $zip -DestinationPath $staging
	if (Test-Path -LiteralPath $final) { Remove-Item -LiteralPath $final -Recurse -Force }
	Move-Item -LiteralPath $staging -Destination $final
	Remove-Item -LiteralPath $zip -Force
	$exe = Join-Path $final 'pwsh.exe'
	Write-Ok "PowerShell $version ($exe)"
	return $exe
}

# Node (Get-NodeDir, Install-NodeToolchain) and Git detection: daily-ao-tools.ps1.

# ---------------------------------------------------------------------------
# Environment for the daily build
# ---------------------------------------------------------------------------
# Every AO_* variable is removed first: an AO session carries AO_SESSION_ID,
# AO_SUPERVISED_PROCESS, a browser token and more, none of which may leak into
# the primary app.
function Set-DailyEnv($cfg, [string]$GoRoot, [string]$NodeDir) {
	Get-ChildItem Env: | Where-Object { $_.Name -like 'AO_*' -or $_.Name -like 'VITE_*' -or $_.Name -eq 'ELECTRON_RUN_AS_NODE' } |
		ForEach-Object { Remove-Item -LiteralPath "Env:$($_.Name)" }
	$env:AO_DATA_DIR = $cfg.DataDir
	$env:AO_RUN_FILE = $cfg.RunFile
	$env:AO_PORT = [string]$cfg.Port
	$env:AO_DEV_ELECTRON_DIR = $cfg.ElectronDir
	# The daemon must outlive Electron. Without this, any Electron exit
	# (restart, crash) is treated as "desktop quit" and stops every agent.
	$env:AO_KEEP_DAEMON = '1'
	# Tells the app where the watcher leaves pending-restart.json: the app shows the
	# "update ready" notice from it and runs the update script from here.
	$env:AO_DAILY_DIR = $cfg.DailyDir
	$env:VITE_WORKOS_CLIENT_ID = $cfg.WorkOsClientId
	if ($cfg.CloudAuthRedirect) { $env:AO_CLOUD_AUTH_REDIRECT = $cfg.CloudAuthRedirect }
	$env:GOPATH = $cfg.GoPath
	$env:GOCACHE = $cfg.GoCache
	$env:GOTOOLCHAIN = 'local'
	$prefix = @()
	if ($GoRoot) { $prefix += (Join-Path $GoRoot 'bin') }
	if ($NodeDir) { $prefix += $NodeDir }
	# The stable daily ao: the daemon pins its own folder first for every session
	# it starts; this keeps `ao` resolving to the daily build when that folder is
	# gone (old dev-* folders are pruned) or the session's own shell reorders PATH.
	if (Test-Path -LiteralPath (Get-DailyCliPath $cfg)) { $prefix += $cfg.BinDir }
	$rest = ($env:Path -split ';') | Where-Object { $_ -and ($prefix -notcontains $_) }
	$env:Path = (@($prefix) + @($rest)) -join ';'
	foreach ($k in $cfg.ExtraEnv.Keys) { Set-Item -LiteralPath "Env:$k" -Value $cfg.ExtraEnv[$k] }
}

# The env the INSTALLED app gets on rollback: no dev overrides. A simulation
# still needs its private paths and port.
function Set-InstalledEnv($cfg) {
	Get-ChildItem Env: | Where-Object { $_.Name -like 'AO_*' -or $_.Name -like 'VITE_*' -or $_.Name -eq 'ELECTRON_RUN_AS_NODE' } |
		ForEach-Object { Remove-Item -LiteralPath "Env:$($_.Name)" }
	if (-not $cfg.IsReal) {
		$env:AO_DATA_DIR = $cfg.DataDir
		$env:AO_RUN_FILE = $cfg.RunFile
		$env:AO_PORT = [string]$cfg.Port
	}
	foreach ($k in $cfg.ExtraEnv.Keys) { Set-Item -LiteralPath "Env:$k" -Value $cfg.ExtraEnv[$k] }
}

# ---------------------------------------------------------------------------
# Checkout and builds
# ---------------------------------------------------------------------------
function Initialize-Checkout($cfg) {
	Assert-GitAvailable
	if (-not (Test-Path -LiteralPath (Join-Path $cfg.Checkout '.git'))) {
		Write-Step "Eigen checkout van $($cfg.Branch) maken in $($cfg.Checkout)"
		$cloneArgs = @('clone', '--quiet', '--branch', $cfg.Branch)
		if ($cfg.ReferenceRepo -and (Test-Path -LiteralPath (Join-Path $cfg.ReferenceRepo '.git'))) {
			# Borrow objects from an existing local clone, then copy them so the
			# daily checkout never depends on it.
			$cloneArgs += @('--reference-if-able', $cfg.ReferenceRepo, '--dissociate')
		}
		# With a stored per-URL token the clone uses it (and only it), like every
		# later fetch; without one git uses whatever login it has (may prompt).
		$tokenArgs = @()
		if (Test-DailyGitHubTokenStored $cfg) { $tokenArgs = @(ConvertTo-GitConfigArgs (Get-DailyCredentialSettings $cfg)) }
		$cloneArgs += @($cfg.RepoUrl, $cfg.Checkout)
		& git @tokenArgs @cloneArgs
		if ($LASTEXITCODE -ne 0) { throw "git clone failed. $(Get-GitAuthHint)" }
	}
	[void](Initialize-DailyGitAuth $cfg)
	# At logon the network may not be up yet; starting on the last fetched
	# origin/main is fine, the update watcher catches up later.
	try { Invoke-Git $cfg @('fetch', '--quiet', 'origin', $cfg.Branch) | Out-Null }
	catch {
		if ("$_" -match [regex]::Escape((Get-GitAuthHint))) { Write-Warn2 "git fetch mislukt: $(Get-GitAuthHint) Verder met de laatst opgehaalde origin/$($cfg.Branch)." }
		else { Write-Warn2 "git fetch mislukt (nog geen netwerk?); verder met de laatst opgehaalde origin/$($cfg.Branch)." }
	}
	Assert-ReviewedCheckout $cfg
}

# Only reviewed code: the checkout must be a clean `main` whose HEAD is on
# origin/main (equal or behind it), never a local commit or edit.
function Assert-ReviewedCheckout($cfg) {
	$branch = (Invoke-Git $cfg @('rev-parse', '--abbrev-ref', 'HEAD')).Trim()
	if ($branch -ne $cfg.Branch) { throw "Daily checkout is on '$branch', expected '$($cfg.Branch)'." }
	$dirty = Invoke-Git $cfg @('status', '--porcelain', '--untracked-files=no')
	if ($dirty) { throw "Daily checkout has local edits; it must only contain reviewed code:`n$dirty" }
	& git -C $cfg.Checkout merge-base --is-ancestor HEAD "origin/$($cfg.Branch)"
	if ($LASTEXITCODE -ne 0) { throw "Daily checkout HEAD is not on origin/$($cfg.Branch) (local commits?)." }
}

function Get-TreeHash($cfg, [string]$Path) {
	$out = & git -C $cfg.Checkout rev-parse "HEAD:$Path" 2>$null
	if ($LASTEXITCODE -ne 0) { return '' }
	return ([string]$out).Trim()
}

function Get-DepsKey($cfg) {
	(Get-TreeHash $cfg 'frontend/package-lock.json') + '+' + (Get-TreeHash $cfg 'packages/product-ui/package-lock.json')
}

function Install-FrontendDeps($cfg, [hashtable]$State, [switch]$Force) {
	$key = Get-DepsKey $cfg
	$electronExe = Join-Path $cfg.Frontend 'node_modules\electron\dist\electron.exe'
	if (-not $Force -and $State['depsKey'] -eq $key -and (Test-Path -LiteralPath $electronExe)) { Write-Ok 'npm-afhankelijkheden actueel'; return }
	Write-Step 'npm-afhankelijkheden installeren (npm ci)'
	$log = Join-Path $cfg.LogDir 'npm-ci.log'
	Invoke-Checked -Exe 'npm' -Arguments @('ci', '--no-audit', '--no-fund') -WorkingDirectory (Join-Path $cfg.Checkout 'packages\product-ui') -LogFile $log
	Invoke-Checked -Exe 'npm' -Arguments @('ci', '--no-audit', '--no-fund') -WorkingDirectory $cfg.Frontend -LogFile $log
	# npm 11 can skip install scripts; Electron's binary arrives through one.
	if (-not (Test-Path -LiteralPath $electronExe)) {
		Invoke-Checked -Exe 'node' -Arguments @('node_modules/electron/install.js') -WorkingDirectory $cfg.Frontend -LogFile $log
	}
	if (-not (Test-Path -LiteralPath $electronExe)) { throw "Electron binary missing after npm ci (log: $log)" }
	$State['depsKey'] = $key
	Write-Ok 'npm-afhankelijkheden geinstalleerd'
}

function Get-DaemonManifestPath($cfg) {
	$manifest = Join-Path $cfg.Frontend 'daemon\dev-daemon.json'
	if (-not (Test-Path -LiteralPath $manifest)) { return $null }
	$p = (Get-Content -Raw -LiteralPath $manifest | ConvertFrom-Json).path
	if ($p -and (Test-Path -LiteralPath $p)) { return $p }
	return $null
}

# Rebuilds only what changed. The daemon in particular: every dev build lands in
# a new frontend\daemon\dev-<time>-<pid> folder, so building when nothing
# changed would only add another binary.
function Update-Builds($cfg, [hashtable]$State, [switch]$ForceDaemon) {
	$log = Join-Path $cfg.LogDir 'build.log'
	$backend = Get-TreeHash $cfg 'backend'
	if ($ForceDaemon -or -not (Get-DaemonManifestPath $cfg) -or $State['daemonBackendTree'] -ne $backend) {
		Write-Step 'Backend (Go daemon) bouwen'
		Invoke-Checked -Exe 'node' -Arguments @('scripts/build-daemon.mjs', '--dev') -WorkingDirectory $cfg.Frontend -LogFile $log
		$State['daemonBackendTree'] = $backend
		Write-Ok "daemon: $(Get-DaemonManifestPath $cfg)"
	} else { Write-Ok 'daemon actueel' }
	# After every backend build (and when nothing changed: repairs a missing
	# copy): the stable ao on the PATH follows the daemon binary.
	Update-DailyCli $cfg $State

	$extKey = (Get-TreeHash $cfg 'frontend/src/browser-extension') + '+' + (Get-TreeHash $cfg 'frontend/scripts/build-browser-extension.mjs')
	if ($State['extensionKey'] -ne $extKey -or -not (Test-Path -LiteralPath (Join-Path $cfg.Frontend 'browser-extension\manifest.json'))) {
		Write-Step 'Browser-extensie bouwen'
		Invoke-Checked -Exe 'node' -Arguments @('scripts/build-browser-extension.mjs') -WorkingDirectory $cfg.Frontend -LogFile $log
		$State['extensionKey'] = $extKey
	}
	# Both are idempotent and return quickly when their output is current.
	Invoke-Checked -Exe 'node' -Arguments @('scripts/ensure-browser-runtime.mjs') -WorkingDirectory $cfg.Frontend -LogFile $log
	Invoke-Checked -Exe 'node' -Arguments @('scripts/build-acp-runtime.mjs', '--dev') -WorkingDirectory $cfg.Frontend -LogFile $log
	Write-Ok 'builds actueel'
}

# ---------------------------------------------------------------------------
# Stable ao command: <daily>\bin\ao.exe, on the user PATH
#
# Why: a session finds `ao` through the PATH its daemon builds, and the daemon
# pins ITS OWN folder first (frontend\daemon\dev-<time>-<pid> for the daily
# build, resources\daemon for the installed app). A session started under the
# installed daemon keeps the installed ao for as long as it lives. A plain
# terminal (no AO session) has no ao at all: neither the user nor the system
# PATH names an AO folder, and the app never adds one on Windows. The dev-*
# folder changes on every backend rebuild, so it cannot be put on the PATH.
# This is a real ao.exe copy that follows the daily build, in a folder that
# does not move; the user PATH gets that folder at the front.
# ---------------------------------------------------------------------------
function Get-DailyCliPath($cfg) { Join-Path $cfg.BinDir 'ao.exe' }

function Get-DailyCliBackupFile($cfg) { Join-Path $cfg.BackupDir 'user-path-before-daily-cli.json' }

# Old copies Windows would not let us delete while a process still ran them.
function Remove-DailyCliLeftovers($cfg) {
	if (-not (Test-Path -LiteralPath $cfg.BinDir)) { return }
	Get-ChildItem -LiteralPath $cfg.BinDir -File -Force -ErrorAction SilentlyContinue |
		Where-Object { $_.Name -like 'ao.exe.old-*' -or $_.Name -like 'ao.exe.new-*' } |
		ForEach-Object { Remove-Item -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue }
}

# Copies the daemon binary the next start will run to <bin>\ao.exe. Windows can
# neither overwrite nor delete an ao.exe that runs (a session may be using it),
# but it can rename it: the new copy is made next to it and swapped in with
# File.Replace (atomic; the old one becomes ao.exe.old-<time>, deleted now or by
# a later run). Never throws: a failed refresh must not fail an update. Returns
# 'created', 'updated', 'current', 'skipped' or 'failed'.
function Sync-DailyCli($cfg) {
	try {
		$src = Get-DaemonManifestPath $cfg
		if (-not $src) { Write-Note 'geen gebouwde daily daemon; de stabiele ao wordt niet bijgewerkt'; return 'skipped' }
		$dest = Get-DailyCliPath $cfg
		New-Item -ItemType Directory -Force -Path $cfg.BinDir | Out-Null
		Remove-DailyCliLeftovers $cfg
		$srcHash = (Get-FileHash -LiteralPath $src -Algorithm SHA256).Hash
		$existed = Test-Path -LiteralPath $dest
		if ($existed -and (Get-FileHash -LiteralPath $dest -Algorithm SHA256).Hash -eq $srcHash) {
			Write-Ok "stabiele ao actueel: $dest"
			return 'current'
		}
		$tmp = Join-Path $cfg.BinDir ('ao.exe.new-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
		try {
			Copy-Item -LiteralPath $src -Destination $tmp -Force
			if ((Get-FileHash -LiteralPath $tmp -Algorithm SHA256).Hash -ne $srcHash) { throw 'de kopie komt niet overeen met de bron' }
			if ($existed) {
				$old = Join-Path $cfg.BinDir ('ao.exe.old-' + (Get-Date -Format 'yyyyMMddHHmmssfff'))
				try { [IO.File]::Replace($tmp, $dest, $old) }
				catch { [IO.File]::Move($dest, $old); [IO.File]::Move($tmp, $dest) }
			} else {
				[IO.File]::Move($tmp, $dest)
			}
		} finally {
			if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue }
		}
		Remove-DailyCliLeftovers $cfg
		$version = ''
		try { $version = [string](@(& $dest version 2>&1)[0]) } catch { }
		Write-Ok "stabiele ao $(if ($existed) { 'bijgewerkt' } else { 'gemaakt' }): $dest$(if ($version) { " ($version)" })"
		return $(if ($existed) { 'updated' } else { 'created' })
	} catch {
		Write-Warn2 "stabiele ao bijwerken mislukt (de update zelf gaat door): $_"
		return 'failed'
	}
}

# --- User PATH (HKCU\Environment\Path), read and written exactly as stored ---------
function Get-UserPathValue($cfg) {
	$none = [pscustomobject]@{ Exists = $false; Raw = ''; Kind = 'ExpandString' }
	$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($cfg.EnvRegKey, $false)
	if (-not $key) { return $none }
	try {
		$raw = $key.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
		if ($null -eq $raw) { return $none }
		return [pscustomobject]@{ Exists = $true; Raw = [string]$raw; Kind = [string]$key.GetValueKind('Path') }
	} finally { $key.Dispose() }
}

function Set-UserPathValue($cfg, [string]$Raw, [string]$Kind) {
	$key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey($cfg.EnvRegKey)
	try { $key.SetValue('Path', $Raw, [Microsoft.Win32.RegistryValueKind]$Kind) } finally { $key.Dispose() }
}

function Remove-UserPathValue($cfg) {
	$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($cfg.EnvRegKey, $true)
	if (-not $key) { return }
	try { $key.DeleteValue('Path', $false) } finally { $key.Dispose() }
}

# Two PATH entries are the same folder when they match after %VAR% expansion,
# quotes, a trailing slash and case.
function ConvertTo-PathKey([string]$Entry) {
	$e = [Environment]::ExpandEnvironmentVariables($Entry.Trim().Trim('"'))
	try { $e = [IO.Path]::GetFullPath($e) } catch { }
	return $e.TrimEnd('\', '/').ToLowerInvariant()
}

function Test-PathEntryIs([string]$Entry, [string]$Dir) {
	if (-not $Entry.Trim()) { return $false }
	return (ConvertTo-PathKey $Entry) -eq (ConvertTo-PathKey $Dir)
}

# Tells running programs (Explorer, so new terminals and Start-menu launches)
# that the environment changed. Without it they keep the old PATH until the next
# sign-in. A simulation never broadcasts.
function Send-EnvironmentChange($cfg) {
	if ($cfg.IsSimulation) { return }
	try {
		if (-not ('AoDaily.Win32Env' -as [type])) {
			Add-Type -Namespace AoDaily -Name Win32Env -MemberDefinition @'
[DllImport("user32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint Msg, UIntPtr wParam, string lParam, uint flags, uint timeout, out UIntPtr result);
'@
		}
		$result = [UIntPtr]::Zero
		# HWND_BROADCAST, WM_SETTINGCHANGE, "Environment", SMTO_ABORTIFHUNG, 5 s per window
		[void][AoDaily.Win32Env]::SendMessageTimeout([IntPtr]0xFFFF, 0x1A, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$result)
	} catch { Write-Warn2 "omgevingswijziging doorgeven mislukt (nieuwe vensters zien de PATH pas na opnieuw aanmelden): $_" }
}

# Puts <bin> at the front of the user PATH. Idempotent: no duplicates, and it
# moves an existing entry to the front. The value before the first change is kept
# in state.json and in backup\user-path-before-daily-cli.json.
function Enable-DailyCliPath($cfg, [hashtable]$State) {
	if (-not $cfg.ManageUserPath) { Write-Note 'andere AO-map: de gebruikers-PATH blijft ongemoeid'; return }
	$cur = Get-UserPathValue $cfg
	# @(if ...) and not "$x = if ...": an empty result of the latter is $null, and
	# .Count on $null throws under StrictMode.
	$segments = @(if ($cur.Exists -and $cur.Raw -ne '') { $cur.Raw -split ';' })
	$rest = @($segments | Where-Object { -not (Test-PathEntryIs $_ $cfg.BinDir) })
	$hits = $segments.Count - $rest.Count
	if ($hits -eq 1 -and $segments.Count -gt 0 -and (Test-PathEntryIs $segments[0] $cfg.BinDir)) {
		Write-Ok "gebruikers-PATH heeft $($cfg.BinDir) al vooraan"
		return
	}
	$info = $State['userPath']
	if (-not $info) {
		$info = @{ addedDir = $cfg.BinDir; at = (Get-Date).ToString('o'); original = @{ exists = $cur.Exists; raw = $cur.Raw; kind = $cur.Kind } }
		# Our entry is there but nothing was recorded (state lost): what is left
		# without it is the best available "original".
		if ($hits -gt 0) { $info['original'] = @{ exists = $true; raw = ($rest -join ';'); kind = $cur.Kind }; $info['reconstructed'] = $true }
	}
	$new = (@($cfg.BinDir) + $rest) -join ';'
	$info['written'] = $new
	# The backup first: if the write fails, nothing is changed and nothing is lost.
	New-Item -ItemType Directory -Force -Path $cfg.BackupDir | Out-Null
	$info | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Get-DailyCliBackupFile $cfg) -Encoding utf8
	$State['userPath'] = $info
	Set-UserPathValue $cfg $new $cur.Kind
	Send-EnvironmentChange $cfg
	Write-Ok "gebruikers-PATH: $($cfg.BinDir) vooraan gezet (nieuwe vensters gebruiken de daily ao; draaiende sessies houden hun oude PATH)"
	if ($cfg.IsReal) {
		foreach ($d in @([Environment]::GetEnvironmentVariable('Path', 'Machine') -split ';')) {
			$dir = [Environment]::ExpandEnvironmentVariables($d.Trim())
			if ($dir -and ((Test-Path -LiteralPath (Join-Path $dir 'ao.exe')) -or (Test-Path -LiteralPath (Join-Path $dir 'ao.cmd')))) {
				Write-Warn2 "de systeem-PATH heeft een eigen ao in $dir; die komt VOOR de gebruikers-PATH en wint dus nog."
			}
		}
	}
}

# Takes <bin> off the user PATH again. When the PATH is still exactly what
# Enable-DailyCliPath wrote, the original value comes back byte for byte (and kind
# and presence). When something else changed it since, only our entry is removed
# and the rest is left alone.
function Disable-DailyCliPath($cfg, [hashtable]$State) {
	if (-not $cfg.ManageUserPath) { return }
	$info = $State['userPath']
	$cur = Get-UserPathValue $cfg
	$changed = $false
	if ($info -and $cur.Exists -and $cur.Raw -ceq [string]$info['written']) {
		$orig = $info['original']
		if ($orig['exists']) { Set-UserPathValue $cfg ([string]$orig['raw']) ([string]$orig['kind']) } else { Remove-UserPathValue $cfg }
		$changed = $true
		Write-Ok 'gebruikers-PATH precies teruggezet zoals hij voor de daily ao was'
	} else {
		$segments = @(if ($cur.Exists -and $cur.Raw -ne '') { $cur.Raw -split ';' })
		$rest = @($segments | Where-Object { -not (Test-PathEntryIs $_ $cfg.BinDir) })
		if ($rest.Count -ne $segments.Count) {
			Set-UserPathValue $cfg ($rest -join ';') $cur.Kind
			$changed = $true
			if ($info) { Write-Warn2 "je gebruikers-PATH is sinds het toevoegen veranderd; alleen $($cfg.BinDir) is eruit gehaald, de rest blijft zoals hij nu is. De oude waarde staat in $(Get-DailyCliBackupFile $cfg)." }
			else { Write-Ok "$($cfg.BinDir) uit de gebruikers-PATH gehaald" }
		} else { Write-Note 'de daily ao stond niet op de gebruikers-PATH' }
	}
	if ($changed) { Send-EnvironmentChange $cfg }
	$State['userPath'] = $null
}

# Deletes <bin>. A running ao.exe there cannot be deleted; the folder then stays
# (the PATH entry is already gone, so nothing finds it) until that process ends.
function Remove-DailyCliDir($cfg) {
	if (-not (Test-Path -LiteralPath $cfg.BinDir)) { return }
	try {
		Remove-Item -LiteralPath $cfg.BinDir -Recurse -Force -ErrorAction Stop
		Write-Ok "map $($cfg.BinDir) verwijderd"
	} catch {
		$holders = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object { $_.ExecutablePath -and (Test-PathInside $_.ExecutablePath $cfg.BinDir) } | ForEach-Object { "PID $($_.ProcessId)" })
		Write-Warn2 "map $($cfg.BinDir) kon niet helemaal weg$(if ($holders) { " (in gebruik door $($holders -join ', '))" }). Zodra die processen stoppen mag je de map zelf verwijderen; niets vindt hem nog via de PATH."
	}
}

# After a backend build: the stable ao follows the daemon binary; and, while the
# daily setup is active, makes sure the user PATH has it.
function Update-DailyCli($cfg, [hashtable]$State) {
	[void](Sync-DailyCli $cfg)
	if ($State['active'] -and (Test-Path -LiteralPath (Get-DailyCliPath $cfg))) {
		try { Enable-DailyCliPath $cfg $State }
		catch { Write-Warn2 "stabiele ao op de gebruikers-PATH zetten mislukt: $_ (herstel: start-daily-ao.ps1 -InstallCli)" }
	}
}

# Rollback: PATH entry and bin folder both go.
function Disable-DailyCli($cfg, [hashtable]$State) {
	Disable-DailyCliPath $cfg $State
	Remove-DailyCliDir $cfg
}

# ---------------------------------------------------------------------------
# Processes (always matched by exact path; never by name alone)
# ---------------------------------------------------------------------------
function Get-ProcessTable { Get-CimInstance Win32_Process | Select-Object ProcessId, ParentProcessId, Name, ExecutablePath, CommandLine, CreationDate }

function Get-DailyProcesses($cfg) {
	$all = Get-ProcessTable
	$electronExe = Join-Path $cfg.Frontend 'node_modules\electron\dist\electron.exe'
	$forgeJs = Join-Path $cfg.Frontend 'node_modules\@electron-forge'
	$agentBrowserDir = Join-Path $cfg.Frontend 'agent-browser'
	[pscustomobject]@{
		Forge            = @($all | Where-Object { $_.Name -eq 'node.exe' -and $_.CommandLine -and $_.CommandLine.IndexOf($forgeJs, [StringComparison]::OrdinalIgnoreCase) -ge 0 })
		ElectronMain     = @($all | Where-Object { (Test-SamePath $_.ExecutablePath $electronExe) -and $_.CommandLine -notmatch '--type=' })
		ElectronChildren = @($all | Where-Object { (Test-SamePath $_.ExecutablePath $electronExe) -and $_.CommandLine -match '--type=' })
		AgentBrowser     = @($all | Where-Object { Test-PathInside $_.ExecutablePath $agentBrowserDir })
	}
}

# Belt and braces for simulations: never signal the real AO's daemon or its
# Electron, whatever the matching above says.
# Empty on the real machine. The comma keeps PowerShell from unrolling the set
# on return: an empty set would arrive as $null, a single PID as a bare int.
function Get-ProtectedPids($cfg) {
	$ids = New-Object System.Collections.Generic.HashSet[int]
	if ($cfg.IsReal) { return , $ids }
	$realRun = Join-Path $cfg.RealAoHome 'running.json'
	if (Test-Path -LiteralPath $realRun) {
		try {
			$info = Get-Content -Raw -LiteralPath $realRun | ConvertFrom-Json
			[void]$ids.Add([int]$info.pid)
			$p = Get-CimInstance Win32_Process -Filter "ProcessId=$([int]$info.pid)"
			if ($p) { [void]$ids.Add([int]$p.ParentProcessId) }
		} catch { }
	}
	return , $ids
}

# Always a bool, never a method call on $null.
function Test-ProtectedPid($cfg, [int]$ProcessId) {
	$set = Get-ProtectedPids $cfg
	if ($null -eq $set) { return $false }
	return [bool]$set.Contains($ProcessId)
}

function Stop-ExactProcess($cfg, [int]$ProcessId, [string]$Label) {
	if (Test-ProtectedPid $cfg $ProcessId) { throw "Refusing to stop PID $ProcessId ($Label): it belongs to the real AO." }
	Stop-Process -Id $ProcessId -Force -ErrorAction SilentlyContinue
	Write-Note "gestopt: $Label (PID $ProcessId)"
}

function Wait-ProcessesGone([int[]]$Ids, [int]$TimeoutSeconds = 15) {
	$deadline = (Get-Date).AddSeconds($TimeoutSeconds)
	while ((Get-Date) -lt $deadline) {
		$alive = @($Ids | Where-Object { Get-Process -Id $_ -ErrorAction SilentlyContinue })
		if (@($alive).Count -eq 0) { return $true }
		Start-Sleep -Milliseconds 300
	}
	return $false
}

# Stops Forge and the daily Electron. The kept-alive daemon and every session
# (pty-hosts) keep running; they are separate processes and are never matched.
function Stop-DailyFrontend($cfg) {
	$p = Get-DailyProcesses $cfg
	$mainIds = @($p.ElectronMain | ForEach-Object { [int]$_.ProcessId })
	foreach ($m in $p.ElectronMain) { Stop-ExactProcess $cfg ([int]$m.ProcessId) 'Electron (daily)' }
	foreach ($f in $p.Forge) { Stop-ExactProcess $cfg ([int]$f.ProcessId) 'Electron Forge / Vite' }
	[void](Wait-ProcessesGone (@($mainIds) + @($p.Forge | ForEach-Object { [int]$_.ProcessId })) 15)
	$left = Get-DailyProcesses $cfg
	foreach ($c in @($left.ElectronChildren) + @($left.AgentBrowser | Where-Object { $mainIds -contains [int]$_.ParentProcessId })) {
		Stop-ExactProcess $cfg ([int]$c.ProcessId) "Electron helper $($c.Name)"
	}
}

# Child processes must not inherit this script's stdout/stderr. Otherwise Forge,
# Electron, the daemon and even pty-hosts keep the caller's pipe open, and a
# caller that waits for the script (an agent's shell) never returns.
function Disable-StdHandleInheritance {
	if (-not ('AoDaily.Kernel32' -as [type])) {
		Add-Type -Namespace AoDaily -Name Kernel32 -MemberDefinition @'
[DllImport("kernel32.dll", SetLastError = true)] public static extern IntPtr GetStdHandle(int nStdHandle);
[DllImport("kernel32.dll", SetLastError = true)] public static extern bool SetHandleInformation(IntPtr hObject, uint dwMask, uint dwFlags);
'@
	}
	foreach ($n in -10, -11, -12) {
		$h = [AoDaily.Kernel32]::GetStdHandle($n)
		if ($h -ne [IntPtr]::Zero -and $h -ne [IntPtr]::new(-1)) { [void][AoDaily.Kernel32]::SetHandleInformation($h, 1, 0) }
	}
}

# Launches Forge (Vite dev servers + Electron) hidden, logging to a file. It is
# started as its own process so closing the window or the session that ran this
# script does not take AO down.
function Start-DailyFrontend($cfg, [string]$GoRoot, [string]$NodeDir) {
	if ((Get-CurrentSessionId) -eq 0) { throw 'Deze Windows-sessie heeft geen bureaublad (sessie 0, gestart voor het aanmelden); een venster zou onzichtbaar zijn.' }
	Disable-StdHandleInheritance
	Set-DailyEnv $cfg $GoRoot $NodeDir
	$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
	$out = Join-Path $cfg.LogDir "forge-$stamp.log"
	$err = Join-Path $cfg.LogDir "forge-$stamp.err.log"
	$forgeStart = Join-Path $cfg.Frontend 'node_modules\@electron-forge\cli\dist\electron-forge-start.js'
	$forgeArgs = @("`"$forgeStart`"")
	if (@($cfg.ElectronArgs).Count -gt 0) { $forgeArgs += @('--') + @($cfg.ElectronArgs) }
	$proc = Start-Process -FilePath (Join-Path $NodeDir 'node.exe') -ArgumentList $forgeArgs `
		-WorkingDirectory $cfg.Frontend -WindowStyle Hidden -RedirectStandardOutput $out -RedirectStandardError $err -PassThru
	Write-Note "Forge PID $($proc.Id); log $out"
	return [pscustomobject]@{ ForgePid = $proc.Id; Log = $out; ErrLog = $err; StartedUtc = (Get-Date).ToUniversalTime() }
}

# ---------------------------------------------------------------------------
# Windows sessions. Before anyone signs in, the boot task runs in session 0:
# no desktop. Agents started then stay in session 0 after sign-in, so anything
# they run (an update restart) must not open the window there.
# ---------------------------------------------------------------------------
function Get-CurrentSessionId { [Diagnostics.Process]::GetCurrentProcess().SessionId }

# The session of this user's desktop (Explorer), or $null when signed out.
function Get-UserDesktopSessionId {
	$me = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
	foreach ($p in Get-CimInstance Win32_Process -Filter "Name='explorer.exe'") {
		if ($p.SessionId -eq 0) { continue }
		$owner = Invoke-CimMethod -InputObject $p -MethodName GetOwnerSid -ErrorAction SilentlyContinue
		if ($owner -and $owner.Sid -eq $me) { return [int]$p.SessionId }
	}
	return $null
}

# Starts the window on the user's desktop. From session 0 the start is handed
# to the "Ayo (daily) openen" task, which Windows runs in the signed-in
# session. Returns $null when nobody is signed in (nothing to show).
function Start-DailyFrontendForUser($cfg, [string]$GoRoot, [string]$NodeDir) {
	if (-not $cfg.FrontendViaTask -and (Get-CurrentSessionId) -ne 0) { return Start-DailyFrontend $cfg $GoRoot $NodeDir }
	if ($null -eq (Get-UserDesktopSessionId)) { return $null }
	$task = Get-ComTask $cfg.FrontendTaskPath $cfg.FrontendTaskName
	if (-not $task) { throw "Taak '$($cfg.FrontendTaskName)' ontbreekt; start Ayo (daily) met de snelkoppeling." }
	$since = (Get-Date).ToUniversalTime()
	[void]$task.Run($null)
	Write-Note "start overgedragen aan taak '$($cfg.FrontendTaskName)' (opent op je bureaublad)"
	return [pscustomobject]@{ ForgePid = $null; Log = $null; ErrLog = $null; StartedUtc = $since; ViaTask = $true }
}

# The daemon alone, without Electron: what the boot task starts before anyone
# signs in. It re-arms the phone listener and tunnel by itself. The Electron
# start at sign-in replaces it gracefully and adopts every session.
function Start-DailyHeadlessDaemon($cfg, [int]$TimeoutSeconds = 180) {
	$exe = Get-DaemonManifestPath $cfg
	if (-not $exe) { throw 'Geen gebouwde daily daemon gevonden; draai start-daily-ao.ps1 -Prepare.' }
	$goRoot = $null
	try { $g = Get-GoRoot $cfg (Get-RequiredGoVersion $cfg); if (Test-Path -LiteralPath (Join-Path $g 'bin\go.exe')) { $goRoot = $g } } catch { }
	$nodeDir = $null
	try { $nodeDir = Get-NodeDir $cfg } catch { Write-Warn2 "Node niet gevonden; agents krijgen de standaard PATH. $_" }
	Disable-StdHandleInheritance
	Set-DailyEnv $cfg $goRoot $nodeDir
	# Agents started before sign-in find the portable pwsh first: the Store
	# alias may not start there.
	$bootPwsh = Get-BootPwsh $cfg
	if ($bootPwsh) { $env:Path = (Split-Path $bootPwsh -Parent) + ';' + $env:Path }
	$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
	$out = Join-Path $cfg.LogDir "daemon-headless-$stamp-$PID.log"
	$err = Join-Path $cfg.LogDir "daemon-headless-$stamp-$PID.err.log"
	$since = (Get-Date).ToUniversalTime()
	$proc = Start-Process -FilePath $exe -ArgumentList 'daemon' -WorkingDirectory $cfg.Frontend -WindowStyle Hidden `
		-RedirectStandardOutput $out -RedirectStandardError $err -PassThru
	Write-Note "daemon zonder venster gestart (PID $($proc.Id)); log $out"
	$ready = Wait-DaemonReady $cfg $since 'daily' $TimeoutSeconds
	return [pscustomobject]@{ Process = $proc; Daemon = $ready; Log = $out; ErrLog = $err }
}

# ---------------------------------------------------------------------------
# Daemon
# ---------------------------------------------------------------------------
function Read-RunFile($cfg) {
	if (-not (Test-Path -LiteralPath $cfg.RunFile)) { return $null }
	try { return Get-Content -Raw -LiteralPath $cfg.RunFile | ConvertFrom-Json } catch { return $null }
}

function Get-DaemonHealth([int]$Port) {
	try { return Invoke-RestMethod -Uri "http://127.0.0.1:$Port/healthz" -TimeoutSec 3 } catch { return $null }
}

# The daemon currently serving this AO home: run file + live /healthz with the same pid.
function Get-CurrentDaemon($cfg) {
	$run = Read-RunFile $cfg
	if (-not $run -or -not $run.port) { return $null }
	$h = Get-DaemonHealth ([int]$run.port)
	if (-not $h -or [int]$h.pid -ne [int]$run.pid) { return $null }
	$kind = if (Test-SamePath $h.executablePath $cfg.InstalledAo) { 'installed' }
		elseif (Test-PathInside $h.executablePath (Join-Path $cfg.Frontend 'daemon')) { 'daily' }
		else { 'other' }
	# A headless daemon (`ao daemon`) writes no owner; never assume optional fields.
	$owner = $run.PSObject.Properties['owner']?.Value
	$startedAt = $run.PSObject.Properties['startedAt']?.Value
	[pscustomobject]@{ Pid = [int]$run.pid; Port = [int]$run.port; Exe = $h.executablePath; Owner = $owner; Kind = $kind; StartedAt = $startedAt }
}

# Graceful stop WITHOUT stopping agents: a plain POST /shutdown is the daemon's
# "replacement" path. Sessions keep running in their detached pty-hosts.
function Stop-DaemonGracefully($cfg, $Daemon, [int]$TimeoutSeconds = 45) {
	if (Test-ProtectedPid $cfg $Daemon.Pid) { throw "Refusing to stop PID $($Daemon.Pid): it is the real AO daemon." }
	Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$($Daemon.Port)/shutdown" -TimeoutSec 10 | Out-Null
	$deadline = (Get-Date).AddSeconds($TimeoutSeconds)
	while ((Get-Date) -lt $deadline) {
		if (-not (Get-Process -Id $Daemon.Pid -ErrorAction SilentlyContinue)) { Write-Ok "daemon $($Daemon.Pid) netjes gestopt"; return }
		Start-Sleep -Milliseconds 500
	}
	throw "Daemon $($Daemon.Pid) did not stop within $TimeoutSeconds s"
}

function Wait-DaemonReady($cfg, [datetime]$SinceUtc, [string]$ExpectKind, [int]$TimeoutSeconds = 300) {
	$deadline = (Get-Date).AddSeconds($TimeoutSeconds)
	while ((Get-Date) -lt $deadline) {
		$d = Get-CurrentDaemon $cfg
		if ($d -and $d.Kind -eq $ExpectKind -and $d.StartedAt -and ([datetime]$d.StartedAt).ToUniversalTime() -ge $SinceUtc.AddSeconds(-2)) {
			try { Invoke-RestMethod -Uri "http://127.0.0.1:$($d.Port)/api/v1/sessions" -TimeoutSec 5 | Out-Null; return $d } catch { }
		}
		Start-Sleep -Seconds 1
	}
	throw "No ready $ExpectKind daemon within $TimeoutSeconds s (run file: $($cfg.RunFile))"
}

# ---------------------------------------------------------------------------
# Sessions
# ---------------------------------------------------------------------------
function Get-Sessions($cfg, [int]$Port) {
	$r = Invoke-RestMethod -Uri "http://127.0.0.1:$Port/api/v1/sessions?includeTerminated=false" -TimeoutSec 15
	if ($r.PSObject.Properties.Name -contains 'data') { return @($r.data) }
	if ($r.PSObject.Properties.Name -contains 'sessions') { return @($r.sessions) }
	return @($r)
}

function Get-SessionActivity($s) {
	foreach ($name in 'activity', 'activityState') {
		if ($s.PSObject.Properties.Name -contains $name) {
			$v = $s.$name
			if ($v -is [string]) { return $v }
			if ($v -and $v.PSObject.Properties.Name -contains 'state') { return [string]$v.state }
		}
	}
	return ''
}

# Live = not terminated and the agent has not exited.
function Get-LiveSessionIds($cfg, [int]$Port) {
	@(Get-Sessions $cfg $Port | Where-Object { -not $_.isTerminated -and (Get-SessionActivity $_) -ne 'exited' } | ForEach-Object { [string]$_.id })
}

# Records which sessions are live before we stop a daemon. Also stored in the
# state file as pendingSessions, so a run that fails half-way (daemon stopped,
# new one not started) still checks and restores them on the next start.
function Save-SessionSnapshot($cfg, [int]$Port, [string]$Label, [hashtable]$State) {
	$ids = @(Get-LiveSessionIds $cfg $Port)
	$file = Join-Path $cfg.DailyDir "sessions-$Label.json"
	@{ takenAt = (Get-Date).ToString('o'); port = $Port; live = $ids } | ConvertTo-Json | Set-Content -LiteralPath $file -Encoding utf8
	if ($State) { $State['pendingSessions'] = $ids; Write-DailyState $cfg $State }
	Write-Note "$(@($ids).Count) lopende sessie(s) vastgelegd ($file)"
	return $ids
}

# After a daemon restart, wait until every session that was live is reported
# live again, up to a deadline. Sessions still exited after that are restored
# (`ao session restore` resumes an exited agent in its existing session).
function Confirm-SessionsSurvived($cfg, [int]$Port, [string[]]$Before, [string]$AoExe, [int]$SettleSeconds = 60, [switch]$NoRestore) {
	if (-not $Before -or @($Before).Count -eq 0) { Write-Ok 'geen lopende sessies om te controleren'; return @() }
	$deadline = (Get-Date).AddSeconds($SettleSeconds)
	do {
		$now = Get-LiveSessionIds $cfg $Port
		$missing = @($Before | Where-Object { $now -notcontains $_ })
		if (@($missing).Count -eq 0) { break }
		Start-Sleep -Seconds 2
	} while ((Get-Date) -lt $deadline)
	if (@($missing).Count -eq 0) { Write-Ok "alle $(@($Before).Count) sessie(s) lopen nog (geen herstel nodig)"; return @() }
	Write-Warn2 "$(@($missing).Count) sessie(s) niet meer actief: $($missing -join ', ')"
	if ($NoRestore) { return $missing }
	foreach ($id in $missing) {
		& $AoExe session restore $id
		if ($LASTEXITCODE -eq 0) { Write-Ok "hersteld: $id" } else { Write-Warn2 "herstel mislukt voor $id (exit $LASTEXITCODE)" }
	}
	return $missing
}

# ---------------------------------------------------------------------------
# Desktop credentials that live beside running.json for the installed app but
# under AO_DATA_DIR for a dev build (cloud sign-in, saved remote machines).
# Only one AO runs at a time, so at every switch the newest copy wins.
# ---------------------------------------------------------------------------
# Which copies Sync-DesktopCredentials would make; changes nothing.
function Get-CredentialSyncPlan($cfg) {
	@(foreach ($name in 'cloud-auth.bin', 'remotes.json') {
		$atHome = Join-Path $cfg.AoHome $name
		$data = Join-Path $cfg.DataDir $name
		$h = Test-Path -LiteralPath $atHome
		$d = Test-Path -LiteralPath $data
		if (-not $h -and -not $d) { continue }
		if ($h -and (-not $d -or (Get-Item -LiteralPath $atHome).LastWriteTimeUtc -gt (Get-Item -LiteralPath $data).LastWriteTimeUtc)) {
			[pscustomobject]@{ Name = $name; From = $atHome; To = $data; Label = "$name -> data\ (nieuwste kopie)" }
		} elseif ($d -and (-not $h -or (Get-Item -LiteralPath $data).LastWriteTimeUtc -gt (Get-Item -LiteralPath $atHome).LastWriteTimeUtc)) {
			[pscustomobject]@{ Name = $name; From = $data; To = $atHome; Label = "$name -> AO-map (nieuwste kopie)" }
		}
	})
}

function Sync-DesktopCredentials($cfg) {
	foreach ($c in Get-CredentialSyncPlan $cfg) {
		New-Item -ItemType Directory -Force -Path (Split-Path $c.To -Parent) | Out-Null
		Copy-Item -LiteralPath $c.From -Destination $c.To -Force
		Write-Note $c.Label
	}
}

# ---------------------------------------------------------------------------
# Installed app
# ---------------------------------------------------------------------------
function Get-InstalledVersion($cfg) {
	if (-not (Test-Path -LiteralPath $cfg.InstalledExe)) { return $null }
	return (Get-Item -LiteralPath $cfg.InstalledExe).VersionInfo.ProductVersion
}

# Installed Electron main processes. On the real machine: any from the install
# dir. In a simulation the REAL installed app runs from the same dir, so only
# the parent of this AO home's daemon counts.
function Get-InstalledMainProcesses($cfg, $Daemon) {
	$all = Get-ProcessTable
	if ($cfg.IsReal) {
		return @($all | Where-Object { (Test-SamePath $_.ExecutablePath $cfg.InstalledExe) -and $_.CommandLine -notmatch '--type=' })
	}
	if (-not $Daemon) { return @() }
	$d = $all | Where-Object { $_.ProcessId -eq $Daemon.Pid } | Select-Object -First 1
	if (-not $d) { return @() }
	return @($all | Where-Object { $_.ProcessId -eq $d.ParentProcessId -and (Test-SamePath $_.ExecutablePath $cfg.InstalledExe) })
}

# Stop the installed AO for the switch, keeping every agent running:
#  1. graceful /shutdown of its daemon (no stopAgents: sessions detach);
#  2. then TERMINATE its Electron instead of quitting it. A normal quit can run
#     a downloaded update on the way out (auto-update is on), and that
#     installer closes every process in the install dir, including the
#     pty-hosts that keep the sessions alive.
function Stop-InstalledForSwitch($cfg, [hashtable]$State) {
	$daemon = Get-CurrentDaemon $cfg
	if (-not $daemon -or $daemon.Kind -ne 'installed') { throw 'The installed AO daemon is not the one serving this AO home; nothing to switch from.' }
	$mains = Get-InstalledMainProcesses $cfg $daemon
	$before = Save-SessionSnapshot $cfg $daemon.Port 'before-switch' $State
	$pending = Join-Path $env:LOCALAPPDATA 'agent-orchestrator-updater\pending'
	if ($cfg.IsReal -and (Test-Path -LiteralPath $pending)) { Write-Warn2 'er staat een gedownloade update klaar; die wordt NIET geinstalleerd omdat de app hard wordt gestopt.' }
	Write-Step "Geinstalleerde daemon (PID $($daemon.Pid)) netjes stoppen; sessies blijven lopen"
	Stop-DaemonGracefully $cfg $daemon
	Start-Sleep -Seconds 3
	$again = Get-CurrentDaemon $cfg
	if ($again) { throw "A daemon came back on port $($again.Port) (PID $($again.Pid)); stop the installed app manually and retry." }
	Write-Step 'Geinstalleerde app sluiten (zonder update-installatie)'
	$all = Get-ProcessTable
	foreach ($m in $mains) {
		$kids = @($all | Where-Object { $_.ParentProcessId -eq $m.ProcessId -and (Test-SamePath $_.ExecutablePath $cfg.InstalledExe) })
		Stop-ExactProcess $cfg ([int]$m.ProcessId) 'Orchestrator.inc (geinstalleerd)'
		foreach ($k in $kids) { Stop-ExactProcess $cfg ([int]$k.ProcessId) 'Orchestrator.inc helper' }
	}
	[void](Wait-ProcessesGone @($mains | ForEach-Object { [int]$_.ProcessId }) 15)
	return $before
}

# ---------------------------------------------------------------------------
# Shortcuts: the installed ones are moved aside (not deleted) and daily ones added.
# ---------------------------------------------------------------------------
# A shortcut that cannot be read (broken, no access) points nowhere.
function Get-ShortcutTarget([string]$Path) {
	try {
		$sh = New-Object -ComObject WScript.Shell
		return [string]$sh.CreateShortcut($Path).TargetPath
	} catch { return $null }
}

# The .lnk files in Dirs that start the installed app; changes nothing.
function Get-InstalledAppLinks($cfg, [string[]]$Dirs) {
	@(foreach ($dir in $Dirs) {
		if (-not $dir -or -not (Test-Path -LiteralPath $dir)) { continue }
		Get-ChildItem -LiteralPath $dir -Filter '*.lnk' -File | Where-Object { Test-SamePath (Get-ShortcutTarget $_.FullName) $cfg.InstalledExe }
	})
}

function Disable-InstalledShortcuts($cfg, [hashtable]$State) {
	$moved = @()
	if ($State['movedShortcuts']) { $moved = @($State['movedShortcuts']) }
	$i = 0
	foreach ($lnk in Get-InstalledAppLinks $cfg $cfg.ShortcutDirs) {
		$dest = Join-Path $cfg.BackupDir ("shortcuts\{0}-{1}" -f $i, $lnk.Name)
		New-Item -ItemType Directory -Force -Path (Split-Path $dest -Parent) | Out-Null
		Move-Item -LiteralPath $lnk.FullName -Destination $dest -Force
		$moved += @{ original = $lnk.FullName; backup = $dest }
		Write-Note "snelkoppeling apart gezet: $($lnk.FullName)"
		$i++
	}
	$State['movedShortcuts'] = $moved
	$created = @()
	foreach ($dir in $cfg.ShortcutDirs) {
		if (-not (Test-Path -LiteralPath $dir)) { continue }
		$path = Join-Path $dir 'Ayo (daily).lnk'
		New-LauncherShortcut $cfg $path @('-Notify')
		$created += $path
	}
	$State['dailyShortcuts'] = $created
}

# The scripts run through `conhost.exe --headless`: no console window at all.
# Plain `pwsh -WindowStyle Hidden` still opens a Windows Terminal window here,
# because Windows Terminal is the default terminal.
function New-LauncherShortcut($cfg, [string]$Path, [string[]]$ExtraArgs, [int]$WindowStyle = 1) {
	$sh = New-Object -ComObject WScript.Shell
	$pwsh = Get-PwshPath $cfg
	$script = Join-Path $cfg.ScriptsDir 'start-daily-ao.ps1'
	$argLine = "--headless `"$pwsh`" -NoProfile -ExecutionPolicy Bypass -File `"$script`""
	if ($cfg.ConfigFile) { $argLine += " -ConfigFile `"$($cfg.ConfigFile)`"" }
	if (@($ExtraArgs).Count -gt 0) { $argLine += ' ' + ($ExtraArgs -join ' ') }
	$s = $sh.CreateShortcut($Path)
	$s.TargetPath = Join-Path $env:SystemRoot 'System32\conhost.exe'
	$s.Arguments = $argLine
	$s.WorkingDirectory = $cfg.ScriptsDir
	$s.WindowStyle = $WindowStyle
	$icon = Join-Path $cfg.Frontend 'assets\icon.ico'
	if (Test-Path -LiteralPath $icon) { $s.IconLocation = $icon }
	$s.Save()
}

# ---------------------------------------------------------------------------
# Login autostart. The installed app's own login items (a Run-key value or a
# Startup-folder shortcut that start agent-orchestrator.exe) are set aside so
# it never starts next to Ayo (daily); rollback puts them back.
# ---------------------------------------------------------------------------
function Get-AutostartLinkPath($cfg) { Join-Path $cfg.StartupDir 'Ayo (daily) autostart.lnk' }

# ---------------------------------------------------------------------------
# Scheduled tasks that start the installed app.
# A task that runs at boot and starts the installed app (found by what its actions
# run, whatever it is called; a user's own "start my tools at boot" task) is REPOINTED: only its
# action that starts the installed app becomes the Ayo (daily) boot start. The
# trigger, account, stored password, settings, permissions and every other
# action stay as they are. Any other such task is disabled. Rollback
# re-registers the saved original definition and checks it is identical.
# Tasks made from an elevated prompt only change with admin rights; then all
# changes go through daily-ao-task-helper.ps1 behind one UAC prompt.
# ---------------------------------------------------------------------------
$script:TaskNs = 'http://schemas.microsoft.com/windows/2004/02/mit/task'
$script:TaskLogonTypes = @{ Password = 1; S4U = 2; InteractiveToken = 3; Group = 4; ServiceAccount = 5; InteractiveTokenOrPassword = 6 }

function Get-TextHash([string]$Text) { [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($Text))) }

function Get-ComTaskFolderPath([string]$TaskPath) { if (-not $TaskPath -or $TaskPath -eq '\') { '\' } else { $TaskPath.TrimEnd('\') } }

function Get-ComTask([string]$TaskPath, [string]$TaskName) {
	$svc = New-Object -ComObject Schedule.Service
	$svc.Connect()
	try { return $svc.GetFolder((Get-ComTaskFolderPath $TaskPath)).GetTask($TaskName) } catch { return $null }
}

function New-TaskXmlNs([xml]$Doc) {
	$ns = [Xml.XmlNamespaceManager]::new($Doc.NameTable)
	$ns.AddNamespace('t', $script:TaskNs)
	# The manager is enumerable; the comma keeps PowerShell from unrolling it.
	return , $ns
}

# Every scheduled task this user can see. In a simulation only the sim's own
# task folder.
function Get-VisibleTasks($cfg) {
	@(if ($cfg.TaskFolder) { Get-ScheduledTask -TaskPath $cfg.TaskFolder -ErrorAction SilentlyContinue } else { Get-ScheduledTask -ErrorAction SilentlyContinue })
}

# The program a task action starts, or $null. Only an Exec action has one. Most
# of Windows' own tasks use COM handler actions, which have no Execute property,
# and strict mode throws on reading a property that does not exist.
function Get-TaskActionCommand($Action) {
	if (-not $Action) { return $null }
	$p = $Action.PSObject.Properties['Execute']
	if (-not $p -or -not $p.Value) { return $null }
	return [Environment]::ExpandEnvironmentVariables(([string]$p.Value).Trim().Trim('"'))
}

# Scheduled tasks that start the installed app (for example a boot task).
function Get-InstalledAppTasks($cfg, $Tasks = $null) {
	if ($null -eq $Tasks) { $Tasks = Get-VisibleTasks $cfg }
	@($Tasks | Where-Object { @($_.Actions | Where-Object { Test-SamePath (Get-TaskActionCommand $_) $cfg.InstalledExe }).Count -gt 0 })
}

function Get-TaskDefinitionInfo([string]$TaskPath, [string]$TaskName) {
	$t = Get-ComTask $TaskPath $TaskName
	if (-not $t) { return $null }
	$doc = [xml]$t.Xml
	$ns = New-TaskXmlNs $doc
	$logonNode = $doc.SelectSingleNode('//t:Principals/t:Principal/t:LogonType', $ns)
	$logonName = if ($logonNode) { $logonNode.InnerText } else { 'InteractiveToken' }
	[pscustomobject]@{
		Xml            = $t.Xml
		XmlHash        = Get-TextHash $t.Xml
		Dacl           = $t.GetSecurityDescriptor(4)
		Enabled        = [bool]$t.Enabled
		LogonTypeName  = $logonName
		LogonType      = $script:TaskLogonTypes[$logonName]
		HasBootTrigger = [bool]$doc.SelectSingleNode('//t:Triggers/t:BootTrigger', $ns)
	}
}

# What the boot task runs instead of the installed app.
function Get-BootLauncher($cfg) {
	$pwsh = Get-BootPwsh $cfg
	if (-not $pwsh) { throw 'PowerShell voor de opstart-taak ontbreekt; draai start-daily-ao.ps1 -Prepare.' }
	$script = Join-Path $cfg.ScriptsDir 'start-daily-ao.ps1'
	$argLine = "--headless `"$pwsh`" -NoProfile -ExecutionPolicy Bypass -File `"$script`" -Boot"
	if ($cfg.ConfigFile) { $argLine += " -ConfigFile `"$($cfg.ConfigFile)`"" }
	[pscustomobject]@{ Command = Join-Path $env:SystemRoot 'System32\conhost.exe'; Arguments = $argLine; WorkingDirectory = $cfg.ScriptsDir }
}

function Test-InstalledAppExec($cfg, $ExecNode, $Ns) {
	$cmd = $ExecNode.SelectSingleNode('t:Command', $Ns)
	return $cmd -and (Test-SamePath ([Environment]::ExpandEnvironmentVariables($cmd.InnerText.Trim().Trim('"'))) $cfg.InstalledExe)
}

# The same definition with only the installed-app action(s) replaced.
function ConvertTo-RepointedTaskXml($cfg, [string]$Xml) {
	$doc = [xml]$Xml
	$ns = New-TaskXmlNs $doc
	$launcher = Get-BootLauncher $cfg
	$n = 0
	foreach ($e in @($doc.SelectNodes('//t:Actions/t:Exec', $ns))) {
		if (-not (Test-InstalledAppExec $cfg $e $ns)) { continue }
		foreach ($child in @($e.ChildNodes)) { if ($child.LocalName -ne 'Command') { [void]$e.RemoveChild($child) } }
		$e.SelectSingleNode('t:Command', $ns).InnerText = $launcher.Command
		# The schema wants Command, Arguments, WorkingDirectory in this order.
		foreach ($name in 'Arguments', 'WorkingDirectory') {
			$node = $doc.CreateElement($name, $script:TaskNs)
			$node.InnerText = $launcher.$name
			[void]$e.AppendChild($node)
		}
		$n++
	}
	if ($n -eq 0) { throw 'No action in this task starts the installed app.' }
	return $doc.OuterXml
}

# The definition without the actions that start the installed app or the boot
# start: this part must be identical before and after a repoint.
function Get-TaskXmlCore($cfg, [string]$Xml) {
	$doc = [xml]$Xml
	$ns = New-TaskXmlNs $doc
	$launcher = (Get-BootLauncher $cfg).Command
	foreach ($e in @($doc.SelectNodes('//t:Actions/t:Exec', $ns))) {
		$cmd = [Environment]::ExpandEnvironmentVariables($e.SelectSingleNode('t:Command', $ns).InnerText.Trim().Trim('"'))
		if ((Test-SamePath $cmd $cfg.InstalledExe) -or (Test-SamePath $cmd $launcher)) { [void]$e.ParentNode.RemoveChild($e) }
	}
	return $doc.OuterXml
}

function Get-TaskAccountName([string]$Xml) {
	$doc = [xml]$Xml
	$node = $doc.SelectSingleNode('//t:Principals/t:Principal/t:UserId', (New-TaskXmlNs $doc))
	if (-not $node) { return [Security.Principal.WindowsIdentity]::GetCurrent().Name }
	$id = $node.InnerText
	if ($id -match '^S-1-') { try { return ([Security.Principal.SecurityIdentifier]$id).Translate([Security.Principal.NTAccount]).Value } catch { } }
	return $id
}

# Whether this process may change a task without UAC: it runs elevated, or the
# task's DACL lets this user (or one of its enabled groups) write it.
function Test-TaskWritableByMe([string]$Dacl) {
	$id = [Security.Principal.WindowsIdentity]::GetCurrent()
	if (([Security.Principal.WindowsPrincipal]$id).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { return $true }
	$mine = @($id.User.Value) + @($id.Groups | ForEach-Object { $_.Value })
	try { $sd = [Security.AccessControl.RawSecurityDescriptor]::new($Dacl) } catch { return $false }
	foreach ($ace in @($sd.DiscretionaryAcl)) {
		# 0x2 = write data, 0x40000000 = generic write.
		if ($ace -is [Security.AccessControl.CommonAce] -and $ace.AceQualifier -eq 'AccessAllowed' -and
			$mine -contains $ace.SecurityIdentifier.Value -and ($ace.AccessMask -band 0x40000002)) { return $true }
	}
	return $false
}

# Errors that mean "Windows wants the task account's password" (or, without
# admin rights, "not allowed"): logon failure, wrong password, account
# information not set, access denied.
function Test-TaskCredentialError($ErrorRecord) {
	$codes = @(-2147023570, -2147024810, -2147216625, -2147024891)
	for ($ex = $ErrorRecord.Exception; $ex; $ex = $ex.InnerException) { if ($codes -contains $ex.HResult) { return $true } }
	return $false
}

# Applies task changes in this process. Ops (hashtables):
#   op = register | enable | disable; path; name
#   xml, logonType, dacl   (register) the full definition and its permissions
#   expectXmlHash          refuse when the task changed since it was read
# A task that runs with a stored password is first re-registered WITHOUT the
# password (Windows keeps one stored password per account). Only if Windows
# refuses, and prompting is allowed, the password is asked once.
function Invoke-TaskOpsNow([object[]]$Ops, [switch]$AllowPasswordPrompt) {
	$svc = New-Object -ComObject Schedule.Service
	$svc.Connect()
	$password = $null
	$usedPassword = $false
	$done = [Collections.Generic.List[string]]::new()
	foreach ($op in $Ops) {
		$label = "$($op.path)$($op.name)"
		$folder = $svc.GetFolder((Get-ComTaskFolderPath $op.path))
		$task = $null
		try { $task = $folder.GetTask($op.name) } catch { }
		# Optional keys by index: after the JSON round trip a missing key would
		# throw under strict mode with dot access.
		if ($op['expectXmlHash']) {
			if (-not $task) { throw "Taak $label bestaat niet meer; er is niets aangepast." }
			if ((Get-TextHash $task.Xml) -ne $op['expectXmlHash']) { throw "Taak $label is veranderd sinds het script hem las; er is niets aangepast." }
		}
		switch ($op.op) {
			'enable' { if (-not $task) { throw "Taak $label bestaat niet." }; $task.Enabled = $true }
			'disable' { $task.Enabled = $false }
			'register' {
				$logon = [int]$op.logonType
				$ok = $false
				if (-not $password) {
					try { [void]$folder.RegisterTask($op.name, $op.xml, 6, $null, $null, $logon, $null); $ok = $true }
					catch { if ($logon -ne 1 -or -not $AllowPasswordPrompt -or -not (Test-TaskCredentialError $_)) { throw } }
				}
				if (-not $ok) {
					$account = Get-TaskAccountName $op.xml
					Write-Host ''
					Write-Host "Windows wil het wachtwoord van $account om taak '$($op.name)' ook zonder aanmelding te laten draaien." -ForegroundColor Yellow
					Write-Host 'Typ het wachtwoord van dit Windows-account: je Microsoft-account, of bij een werk- of schoolaccount je werkwachtwoord (je pincode of Windows Hello werkt hier niet).'
					Write-Host 'Alleen Enter (leeg) = annuleren; dan verandert er niets.'
					for ($i = 1; -not $ok; $i++) {
						if (-not $password) {
							$password = Read-Host -AsSecureString 'Wachtwoord'
							if ($password.Length -eq 0) { throw 'Geannuleerd: geen wachtwoord ingevuld. Er is niets aangepast.' }
						}
						$plain = [Net.NetworkCredential]::new('', $password).Password
						try { [void]$folder.RegisterTask($op.name, $op.xml, 6, $account, $plain, 1, $null); $ok = $true; $usedPassword = $true }
						catch {
							$password = $null
							if ($i -ge 3 -or -not (Test-TaskCredentialError $_)) { throw }
							Write-Host 'Dat wachtwoord werkt niet. Probeer het opnieuw.' -ForegroundColor Yellow
						} finally { $plain = $null }
					}
				}
				if ($op['dacl']) {
					$t2 = $folder.GetTask($op.name)
					# 0x10: do not add an entry for the task's account.
					if ($t2.GetSecurityDescriptor(4) -ne $op['dacl']) { $t2.SetSecurityDescriptor($op['dacl'], 0x10) }
				}
			}
			default { throw "Onbekende taak-actie '$($op.op)'." }
		}
		$done.Add($label)
	}
	[pscustomobject]@{ Done = @($done); UsedPassword = $usedPassword }
}

# Applies task changes: directly when the user may change the task (own tasks,
# simulation), otherwise through the elevated helper: ONE UAC prompt for all
# remaining changes, and a password question only if Windows insists.
function Invoke-TaskOps($cfg, [object[]]$Ops, [string[]]$Purpose) {
	if (@($Ops).Count -eq 0) { return }
	$remaining = [Collections.Generic.List[object]]::new()
	foreach ($o in $Ops) { $remaining.Add($o) }
	if (-not $cfg.ForceTaskHelper) {
		while ($remaining.Count -gt 0) {
			try { [void](Invoke-TaskOpsNow @($remaining[0])); $remaining.RemoveAt(0) }
			catch { if (Test-TaskCredentialError $_) { break }; throw }
		}
	}
	if ($remaining.Count -eq 0) { return }
	$req = Join-Path $cfg.DailyDir ("task-request-{0}-{1}.json" -f (Get-Date -Format 'yyyyMMddHHmmss'), $PID)
	$res = "$req.result.json"
	@{ purpose = @($Purpose); ops = @($remaining) } | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $req -Encoding utf8
	try {
		# The helper next to this library, so both are always the same version.
		$helper = Join-Path $PSScriptRoot 'daily-ao-task-helper.ps1'
		$argList = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$helper`"", '-Request', "`"$req`"")
		$pwsh = Get-PwshPath $cfg
		if ($cfg.IsSimulation) {
			$p = Start-Process -FilePath $pwsh -ArgumentList $argList -WindowStyle Hidden -Wait -PassThru
		} else {
			Write-Note 'Windows vraagt nu toestemming (UAC) om je geplande taak aan te passen. Klik Ja.'
			try { $p = Start-Process -FilePath $pwsh -ArgumentList $argList -Verb RunAs -Wait -PassThru }
			catch { throw 'Geen toestemming gegeven (UAC); er is niets aangepast.' }
		}
		$result = if (Test-Path -LiteralPath $res) { Get-Content -Raw -LiteralPath $res | ConvertFrom-Json } else { $null }
		if (-not $result -or -not $result.ok) { throw "Taak aanpassen mislukt: $(if ($result) { $result.error } else { "geen resultaat (exit $($p.ExitCode))" })" }
		if ($result.usedPassword) { Write-Ok 'taak aangepast met je wachtwoord (Windows bewaart het zoals voorheen)' }
	} finally {
		Remove-Item -LiteralPath $req, $res -Force -ErrorAction SilentlyContinue
	}
}

# A task record from the switch that did not actually change (UAC refused).
function Test-TaskUnchanged($Record) {
	$now = Get-TaskDefinitionInfo $Record.path $Record.name
	if (-not $now) { return $false }
	if ($Record.mode -eq 'repointed') { return $now.XmlHash -eq $Record.originalXmlHash }
	return $now.Enabled
}

function Confirm-TaskChange($cfg, $Record) {
	$label = "$($Record.path)$($Record.name)"
	$now = Get-TaskDefinitionInfo $Record.path $Record.name
	if ($Record.mode -ne 'repointed') {
		if ($now.Enabled) { throw "Taak $label staat nog aan." }
		Write-Note "taak die de geinstalleerde app start uitgezet: $label"
		return
	}
	$doc = [xml]$now.Xml
	$ns = New-TaskXmlNs $doc
	$launcher = Get-BootLauncher $cfg
	$hits = @($doc.SelectNodes('//t:Actions/t:Exec', $ns) | Where-Object { Test-SamePath $_.SelectSingleNode('t:Command', $ns).InnerText $launcher.Command })
	if (@($hits).Count -eq 0) { throw "Taak $label start Ayo (daily) niet; controleer hem in Taakplanner." }
	if ((Get-TaskXmlCore $cfg $now.Xml) -ne (Get-TaskXmlCore $cfg $Record.originalXml)) {
		Write-Warn2 "taak $label wijst naar Ayo (daily), maar Windows heeft ook iets anders in de definitie veranderd. Origineel: $($Record.originalXmlFile)"
	} elseif ($now.Dacl -ne $Record.originalDacl) {
		Write-Warn2 "taak $label wijst naar Ayo (daily), maar de rechten op de taak zijn veranderd."
	} else {
		Write-Ok "taak $label start bij het opstarten nu Ayo (daily) zonder venster; al het andere is ongewijzigd"
	}
	$Record['appliedXmlHash'] = $now.XmlHash
}

# Puts back every task the switch changed: a repointed task gets its original
# definition back (checked identical), a disabled one is enabled again.
function Restore-InstalledTasks($cfg, [hashtable]$State) {
	$saved = $State['installedLoginItems']
	if (-not $saved -or -not $saved.ContainsKey('tasks')) { return }
	$records = @($saved.tasks | Where-Object { $_ })
	if (@($records).Count -eq 0) { return }
	$ops = @(); $purpose = @()
	foreach ($r in $records) {
		$label = "$($r.path)$($r.name)"
		$mode = if ($r.ContainsKey('mode')) { $r.mode } else { 'disabled' }
		if ($mode -eq 'repointed') {
			$ops += @{ op = 'register'; path = $r.path; name = $r.name; xml = $r.originalXml; logonType = $r.logonType; dacl = $r.originalDacl }
			$purpose += "Taak '$label': precies terugzetten zoals hij was (start weer de geinstalleerde app)."
		} elseif (Get-ComTask $r.path $r.name) {
			$ops += @{ op = 'enable'; path = $r.path; name = $r.name }
			$purpose += "Taak '$label': weer aanzetten."
		} else { Write-Warn2 "taak $label bestaat niet meer" }
	}
	Invoke-TaskOps $cfg $ops $purpose
	foreach ($r in $records) {
		$label = "$($r.path)$($r.name)"
		$now = Get-TaskDefinitionInfo $r.path $r.name
		if (-not $now) { continue }
		if ($r.ContainsKey('mode') -and $r.mode -eq 'repointed') {
			if ($now.Xml -ceq $r.originalXml -and $now.Dacl -eq $r.originalDacl) { Write-Ok "taak $label precies teruggezet (definitie en rechten identiek)" }
			else { Write-Warn2 "taak $label is teruggezet maar wijkt af van het origineel; vergelijk met $($r.originalXmlFile)" }
		} elseif ($now.Enabled) { Write-Ok "taak $label staat weer aan" }
	}
	$saved.tasks = @()
	$State['installedLoginItems'] = $saved
	Write-DailyState $cfg $State
}

# "Ayo (daily) openen": an on-demand task that Windows runs in the signed-in
# user's session. Processes without a desktop (session 0) use it to open the
# window. It is the user's own task: no admin rights needed.
function Register-DailyFrontendTask($cfg) {
	$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
	$pwsh = Get-PwshPath $cfg
	$argLine = "--headless `"$pwsh`" -NoProfile -ExecutionPolicy Bypass -File `"$(Join-Path $cfg.ScriptsDir 'start-daily-ao.ps1')`" -Notify"
	if ($cfg.ConfigFile) { $argLine += " -ConfigFile `"$($cfg.ConfigFile)`"" }
	$x = { param($s) [Security.SecurityElement]::Escape([string]$s) }
	$xml = @"
<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="$($script:TaskNs)">
  <RegistrationInfo><Description>$(& $x 'Opent Ayo (daily) op je bureaublad wanneer een update of herstart dat vraagt vanuit een proces zonder bureaublad (gestart voor het aanmelden). De rollback haalt deze taak weg.')</Description></RegistrationInfo>
  <Principals><Principal id="Author"><UserId>$sid</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><ExecutionTimeLimit>PT1H</ExecutionTimeLimit><Enabled>true</Enabled></Settings>
  <Actions Context="Author"><Exec><Command>$(& $x (Join-Path $env:SystemRoot 'System32\conhost.exe'))</Command><Arguments>$(& $x $argLine)</Arguments><WorkingDirectory>$(& $x $cfg.ScriptsDir)</WorkingDirectory></Exec></Actions>
</Task>
"@
	$svc = New-Object -ComObject Schedule.Service
	$svc.Connect()
	[void]$svc.GetFolder((Get-ComTaskFolderPath $cfg.FrontendTaskPath)).RegisterTask($cfg.FrontendTaskName, $xml, 6, $null, $null, 3, $null)
}

function Unregister-DailyFrontendTask($cfg) {
	if (-not (Get-ComTask $cfg.FrontendTaskPath $cfg.FrontendTaskName)) { return }
	$svc = New-Object -ComObject Schedule.Service
	$svc.Connect()
	$svc.GetFolder((Get-ComTaskFolderPath $cfg.FrontendTaskPath)).DeleteTask($cfg.FrontendTaskName, 0)
	Write-Note "taak '$($cfg.FrontendTaskName)' verwijderd"
}

# What the switch does with scheduled tasks that start the installed app;
# changes nothing. Boot tasks are repointed, others disabled.
function Get-TaskSwitchPlan($cfg, $Tasks = $null) {
	$ops = @(); $records = @(); $purpose = @()
	foreach ($t in Get-InstalledAppTasks $cfg $Tasks) {
		if ($t.State -eq 'Disabled') { continue }
		$info = Get-TaskDefinitionInfo $t.TaskPath $t.TaskName
		if (-not $info) { continue }
		$label = "$($t.TaskPath)$($t.TaskName)"
		if ($info.HasBootTrigger -or $cfg.SimBootTasks -contains $t.TaskName) {
			$file = Join-Path $cfg.BackupDir ("tasks\{0}.original.xml" -f ($label.Trim('\') -replace '[\\/:*?"<>|]', '_'))
			$ops += @{ op = 'register'; path = $t.TaskPath; name = $t.TaskName; xml = (ConvertTo-RepointedTaskXml $cfg $info.Xml); logonType = $info.LogonType; expectXmlHash = $info.XmlHash; dacl = $info.Dacl }
			$records += @{ path = $t.TaskPath; name = $t.TaskName; mode = 'repointed'; originalXml = $info.Xml; originalXmlHash = $info.XmlHash; originalXmlFile = $file; originalDacl = $info.Dacl; logonType = $info.LogonType }
			$purpose += "Taak '$label' (draait bij het opstarten van de pc): de actie die de geinstalleerde app start, start voortaan Ayo (daily) zonder venster. Trigger, account, opgeslagen wachtwoord, instellingen en de andere actie(s) blijven gelijk."
		} else {
			$ops += @{ op = 'disable'; path = $t.TaskPath; name = $t.TaskName; expectXmlHash = $info.XmlHash }
			$records += @{ path = $t.TaskPath; name = $t.TaskName; mode = 'disabled'; originalXmlHash = $info.XmlHash }
			$purpose += "Taak '$label': uitzetten (die start de geinstalleerde app)."
		}
	}
	[pscustomobject]@{ Ops = @($ops); Records = @($records); Purpose = @($purpose) }
}

# Run-key values that start the installed app; changes nothing.
function Get-InstalledRunValues($cfg) {
	if (-not (Test-Path -LiteralPath $cfg.RunKey)) { return @() }
	$exeName = Split-Path $cfg.InstalledExe -Leaf
	$key = Get-Item -LiteralPath $cfg.RunKey
	@(foreach ($name in $key.GetValueNames()) {
		$data = [string]$key.GetValue($name)
		if ($data.IndexOf($cfg.InstalledExe, [StringComparison]::OrdinalIgnoreCase) -ge 0 -or $data.IndexOf("\$exeName", [StringComparison]::OrdinalIgnoreCase) -ge 0) {
			@{ name = $name; data = $data }
		}
	})
}

function Enable-DailyAutostart($cfg, [hashtable]$State) {
	$saved = @{ runValues = @(); startupLinks = @(); tasks = @() }
	if ($State['installedLoginItems']) { $saved = $State['installedLoginItems']; if (-not $saved.ContainsKey('tasks')) { $saved['tasks'] = @() } }
	# Find everything before changing anything: a scan that fails then leaves
	# the machine as it was.
	$plan = Get-TaskSwitchPlan $cfg
	$runValues = Get-InstalledRunValues $cfg
	$startupLinks = Get-InstalledAppLinks $cfg @($cfg.StartupDir)
	# First the scheduled tasks: if that needs a UAC prompt and it is refused,
	# nothing else has changed yet.
	if (@($plan.Ops).Count -gt 0) {
		foreach ($r in @($plan.Records | Where-Object { $_.mode -eq 'repointed' })) {
			New-Item -ItemType Directory -Force -Path (Split-Path $r.originalXmlFile -Parent) | Out-Null
			Set-Content -LiteralPath $r.originalXmlFile -Value $r.originalXml -Encoding unicode -NoNewline
		}
		# Recorded before the change, so a rollback can always put them back. A
		# task already recorded by an earlier (half-finished) switch keeps its
		# first record: that one holds the true original.
		$have = @($saved.tasks | Where-Object { $_ } | ForEach-Object { "$($_.path)|$($_.name)" })
		$saved.tasks = @($saved.tasks | Where-Object { $_ }) + @($plan.Records | Where-Object { $have -notcontains "$($_.path)|$($_.name)" })
		$State['installedLoginItems'] = $saved
		Write-DailyState $cfg $State
		try { Invoke-TaskOps $cfg $plan.Ops $plan.Purpose }
		catch {
			$saved.tasks = @($saved.tasks | Where-Object { $_ -and -not (Test-TaskUnchanged $_) })
			$State['installedLoginItems'] = $saved
			Write-DailyState $cfg $State
			throw
		}
		foreach ($r in $plan.Records) { Confirm-TaskChange $cfg $r }
		Write-DailyState $cfg $State
	}
	Register-DailyFrontendTask $cfg
	foreach ($v in $runValues) {
		$saved.runValues = @($saved.runValues) + @($v)
		Remove-ItemProperty -LiteralPath $cfg.RunKey -Name $v.name
		Write-Note "aanmeld-item van de geinstalleerde app uitgezet: Run\$($v.name)"
	}
	foreach ($lnk in $startupLinks) {
		$dest = Join-Path $cfg.BackupDir "startup\$($lnk.Name)"
		New-Item -ItemType Directory -Force -Path (Split-Path $dest -Parent) | Out-Null
		Move-Item -LiteralPath $lnk.FullName -Destination $dest -Force
		$saved.startupLinks = @($saved.startupLinks) + @(@{ original = $lnk.FullName; backup = $dest })
		Write-Note "aanmeld-item van de geinstalleerde app uitgezet: $($lnk.FullName)"
	}
	if (@($saved.runValues).Count -eq 0 -and @($saved.startupLinks).Count -eq 0 -and @($saved.tasks | Where-Object { $_ }).Count -eq 0) { Write-Note 'de geinstalleerde app had geen eigen aanmeld-item' }
	$State['installedLoginItems'] = $saved
	New-Item -ItemType Directory -Force -Path $cfg.StartupDir | Out-Null
	$link = Get-AutostartLinkPath $cfg
	New-LauncherShortcut $cfg $link @('-AutoStart') 7
	$State['autostartLink'] = $link
	Write-Ok "Ayo (daily) start voortaan bij aanmelden ($link)"
}

function Disable-DailyAutostart($cfg, [hashtable]$State) {
	# Tasks first (may need UAC); the rollback normally did this already.
	Restore-InstalledTasks $cfg $State
	Unregister-DailyFrontendTask $cfg
	$link = if ($State['autostartLink']) { $State['autostartLink'] } else { Get-AutostartLinkPath $cfg }
	if (Test-Path -LiteralPath $link) { Remove-Item -LiteralPath $link -Force; Write-Note "autostart van Ayo (daily) verwijderd" }
	$saved = $State['installedLoginItems']
	if ($saved) {
		foreach ($v in @($saved.runValues)) {
			if (-not $v) { continue }
			if (-not (Test-Path -LiteralPath $cfg.RunKey)) { New-Item -Path $cfg.RunKey -Force | Out-Null }
			New-ItemProperty -LiteralPath $cfg.RunKey -Name $v.name -Value $v.data -PropertyType String -Force | Out-Null
			Write-Note "aanmeld-item van de geinstalleerde app teruggezet: Run\$($v.name)"
		}
		foreach ($l in @($saved.startupLinks)) {
			if ($l -and (Test-Path -LiteralPath $l.backup)) {
				Move-Item -LiteralPath $l.backup -Destination $l.original -Force
				Write-Note "aanmeld-item van de geinstalleerde app teruggezet: $($l.original)"
			}
		}
	}
	$State['installedLoginItems'] = $null
	$State['autostartLink'] = $null
}

# Windows has no tray for this app (the tray is macOS-only), so "in the
# background" means: window minimized to the taskbar, without stealing focus.
function Hide-DailyWindows($cfg, [int]$TimeoutSeconds = 30) {
	if (-not ('AoDaily.User32' -as [type])) {
		Add-Type -Namespace AoDaily -Name User32 -MemberDefinition @'
public delegate bool EnumProc(IntPtr h, IntPtr l);
[DllImport("user32.dll")] public static extern bool EnumWindows(EnumProc f, IntPtr l);
[DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
[DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
[DllImport("user32.dll")] public static extern bool IsIconic(IntPtr h);
[DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr h, int c);
'@
	}
	$deadline = (Get-Date).AddSeconds($TimeoutSeconds)
	do {
		$mains = @((Get-DailyProcesses $cfg).ElectronMain | ForEach-Object { [int]$_.ProcessId })
		$done = 0
		$cb = [AoDaily.User32+EnumProc] {
			param($h, $l)
			$id = [uint32]0
			[void][AoDaily.User32]::GetWindowThreadProcessId($h, [ref]$id)
			if ($mains -contains [int]$id -and [AoDaily.User32]::IsWindowVisible($h)) {
				if (-not [AoDaily.User32]::IsIconic($h)) { [void][AoDaily.User32]::ShowWindow($h, 7) }
				$script:minimizedCount++
			}
			return $true
		}
		$script:minimizedCount = 0
		[void][AoDaily.User32]::EnumWindows($cb, [IntPtr]::Zero)
		$done = $script:minimizedCount
		if ($done -gt 0) { Write-Ok 'venster geminimaliseerd'; return $true }
		Start-Sleep -Seconds 1
	} while ((Get-Date) -lt $deadline)
	return $false
}

function Show-Notice([string]$Text) {
	try {
		Add-Type -AssemblyName System.Windows.Forms
		$n = New-Object System.Windows.Forms.NotifyIcon
		$n.Icon = [System.Drawing.SystemIcons]::Information
		$n.Visible = $true
		$n.ShowBalloonTip(15000, 'Ayo (daily)', $Text, [System.Windows.Forms.ToolTipIcon]::Info)
		Start-Sleep -Seconds 16
		$n.Dispose()
	} catch { }
}

# Named per AO home so a simulation never blocks the real one.
function Get-AoHomeKey($cfg) {
	[Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($cfg.AoHome.ToLowerInvariant()))).Substring(0, 16)
}

# A lock shared by every Windows session of this user (the boot task runs in
# session 0, the sign-in start in session 1). Explicit access for the user:
# the default would follow each logon and could lock the other session out.
function New-DailyMutex($cfg, [string]$Name) {
	$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
	$sec = [Security.AccessControl.MutexSecurity]::new()
	$sec.AddAccessRule([Security.AccessControl.MutexAccessRule]::new($sid, [Security.AccessControl.MutexRights]::FullControl, [Security.AccessControl.AccessControlType]::Allow))
	$created = $false
	return [Threading.MutexAcl]::Create($false, "Global\$Name-$(Get-AoHomeKey $cfg)", [ref]$created, $sec)
}

function Enter-DailyMutex([Threading.Mutex]$Mutex, [int]$TimeoutMs) {
	try { return $Mutex.WaitOne($TimeoutMs) } catch [Threading.AbandonedMutexException] { return $true }
}

function Restore-InstalledShortcuts($cfg, [hashtable]$State) {
	foreach ($p in @($State['dailyShortcuts'])) { if ($p -and (Test-Path -LiteralPath $p)) { Remove-Item -LiteralPath $p -Force } }
	foreach ($m in @($State['movedShortcuts'])) {
		if ($m -and (Test-Path -LiteralPath $m.backup)) {
			Move-Item -LiteralPath $m.backup -Destination $m.original -Force
			Write-Note "snelkoppeling teruggezet: $($m.original)"
		}
	}
	$State['dailyShortcuts'] = @()
	$State['movedShortcuts'] = @()
}

# ---------------------------------------------------------------------------
# Switch check. `start-daily-ao.ps1 -SwitchFromInstalled -CheckOnly` runs every
# detection step of the switch against this machine and prints what the switch
# would do. It changes nothing: no fetch, install, build, task, registry,
# shortcut, state or credential write. The real switch runs the same check
# (-Preflight, without the build part it just did) and stops before changing
# anything when a check fails.
# ---------------------------------------------------------------------------
function Invoke-SwitchCheck($cfg, [hashtable]$State, [switch]$Preflight) {
	$problems = [Collections.Generic.List[string]]::new()
	$fail = { param([string]$Message) Write-Warn2 $Message; $problems.Add($Message) }
	$readOnly = if ($Preflight) { '' } else { ' (alleen lezen; er verandert niets)' }
	Write-Step "Controle overstap$readOnly | AO-map $($cfg.AoHome) | poort $($cfg.Port) | Windows-sessie $(Get-CurrentSessionId)$(if ($cfg.IsSimulation) { ' | SIMULATIE' })"
	if ((Get-CurrentSessionId) -eq 0) { & $fail 'Deze Windows-sessie heeft geen bureaublad (sessie 0); overstappen kan alleen vanuit je eigen sessie.' }

	# --- What runs now ---------------------------------------------------------
	Write-Step 'Wat draait er'
	$prior = @()
	if ($State['installedLoginItems'] -and $State['installedLoginItems'].ContainsKey('tasks')) { $prior = @($State['installedLoginItems']['tasks'] | Where-Object { $_ }) }
	if ($State['active']) { Write-Note 'de daily setup staat al aan (eerder overgestapt); de overstap vult alleen aan wat ontbreekt' }
	elseif ($prior.Count -gt 0 -or $State['autostartLink']) { Write-Note 'een eerdere overstap is half gelukt; deze run maakt hem af en slaat over wat al klaar is' }
	$running = Get-DailyProcesses $cfg
	if (@($running.ElectronMain).Count -gt 0) { & $fail "Ayo (daily) draait al (Electron PID $($running.ElectronMain[0].ProcessId)); de overstap zou niets doen." }
	else { Write-Ok 'Ayo (daily) draait niet' }
	$daemon = Get-CurrentDaemon $cfg
	$mains = @(Get-InstalledMainProcesses $cfg $daemon)
	if (-not $daemon) { Write-Note 'er draait geen AO-daemon; de overstap zet dan alleen snelkoppelingen en autostart om' }
	elseif ($daemon.Kind -eq 'installed') { Write-Ok "geinstalleerde daemon: PID $($daemon.Pid), poort $($daemon.Port), $($daemon.Exe)" }
	elseif ($daemon.Kind -eq 'other') { & $fail "Een andere AO-daemon ($($daemon.Exe)) gebruikt $($cfg.RunFile). Stop die eerst." }
	else { Write-Note "er draait al een daily daemon (PID $($daemon.Pid)); het nieuwe venster neemt die over" }
	if ($mains.Count -gt 0 -and -not ($daemon -and $daemon.Kind -eq 'installed')) { & $fail 'De geinstalleerde app draait zonder daemon. Sluit hem en start opnieuw.' }
	elseif ($mains.Count -gt 0) { Write-Ok "geinstalleerde app: Electron PID $(($mains | ForEach-Object { $_.ProcessId }) -join ', '), versie $(Get-InstalledVersion $cfg)" }
	if ($daemon -and $daemon.Kind -eq 'installed') {
		$live = @(Get-LiveSessionIds $cfg $daemon.Port)
		Write-Ok "$($live.Count) lopende sessie(s); de overstap legt ze vast en controleert ze daarna"
		if ($cfg.IsReal -and (Test-Path -LiteralPath (Join-Path $env:LOCALAPPDATA 'agent-orchestrator-updater\pending'))) {
			Write-Note 'er staat een gedownloade update van de geinstalleerde app klaar; die wordt NIET geinstalleerd (de app wordt hard gestopt)'
		}
	}

	# --- Checkout and builds (the switch itself does these first) --------------
	if (-not $Preflight) {
		Write-Step 'Checkout en builds (zonder ophalen, installeren of bouwen)'
		try { Assert-GitAvailable; Write-Ok "$([string](& git --version))" } catch { & $fail "$_" }
		try {
			Assert-ReviewedCheckout $cfg
			$head = ([string](Invoke-Git $cfg @('rev-parse', 'HEAD'))).Trim()
			Write-Ok "checkout $($cfg.Branch) op $($head.Substring(0, 12)), schoon, op origin/$($cfg.Branch)"
			$lsRemote = Invoke-GitText $cfg @('-C', $cfg.Checkout, 'ls-remote', 'origin', "refs/heads/$($cfg.Branch)")
			$remote = if ($lsRemote.ExitCode -eq 0 -and $lsRemote.Output -match '(?m)^([0-9a-f]{40})\s') { $Matches[1] } else { '' }
			if ($remote -and $remote -ne $head) { Write-Note "op GitHub is $($cfg.Branch) nu $($remote.Substring(0, 12)); draai eerst update-daily-ao.ps1 -Yes om die te bouwen" }
			elseif ($remote) { Write-Ok "gelijk aan $($cfg.Branch) op GitHub" }
			else { Write-Note "GitHub niet bereikbaar; $($cfg.Branch) daar niet vergeleken" }
		} catch { & $fail "$_" }
		try {
			$goVersion = Get-RequiredGoVersion $cfg
			if (Test-Path -LiteralPath (Join-Path (Get-GoRoot $cfg $goVersion) 'bin\go.exe')) { Write-Ok "Go $goVersion aanwezig" }
			else { Write-Note "Go $goVersion ontbreekt; de overstap downloadt het eerst (go.dev, met checksum)" }
		} catch { & $fail "$_" }
		try {
			$nodeFound = Get-NodeCandidate $cfg
			if ($nodeFound) { Write-Ok "Node $(& (Join-Path $nodeFound.Dir 'node.exe') -v) ($($nodeFound.Source))$(if (-not $nodeFound.Exact) { "; CI gebruikt Node $($script:DailyDefaults.NodeMajor), de overstap installeert een losse Node $($script:DailyDefaults.NodeMajor)" })" }
			else { Write-Note "Node $($script:DailyDefaults.NodeMajor) ontbreekt; de overstap installeert eerst een losse Node onder $($cfg.ToolsDir) (nodejs.org, met checksum)" }
		} catch { & $fail "$_" }
		Write-GitHubAccessSummary $cfg
		$electronExe = Join-Path $cfg.Frontend 'node_modules\electron\dist\electron.exe'
		if ($State['depsKey'] -eq (Get-DepsKey $cfg) -and (Test-Path -LiteralPath $electronExe)) { Write-Ok 'npm-afhankelijkheden actueel' }
		else { Write-Note 'npm-afhankelijkheden niet actueel; de overstap draait eerst npm ci (1-3 minuten)' }
		$aoExe = Get-DaemonManifestPath $cfg
		if ($aoExe -and $State['daemonBackendTree'] -eq (Get-TreeHash $cfg 'backend')) { Write-Ok "daemon actueel: $(@(& $aoExe version 2>&1)[0])" }
		else { Write-Note 'daemon niet actueel; de overstap bouwt hem eerst (ongeveer 1 minuut)' }
	}
	$bootPwsh = Get-BootPwsh $cfg
	if ($bootPwsh) { Write-Ok "PowerShell voor de opstart-taak: $bootPwsh" }
	elseif ($Preflight) { & $fail 'losse PowerShell voor de opstart-taak ontbreekt' }
	else { Write-Note 'losse PowerShell voor de opstart-taak ontbreekt; de overstap downloadt die eerst (GitHub, met checksum)' }

	# --- Scheduled tasks ---------------------------------------------------------
	Write-Step 'Geplande taken'
	$tasks = @(Get-VisibleTasks $cfg)
	$kinds = @($tasks | ForEach-Object { $_.Actions } | Group-Object { $_.CimClass.CimClassName -replace '^MSFT_Task', '' -replace 'Action$', '' } |
		Sort-Object Count -Descending | ForEach-Object { "$($_.Count) $($_.Name)" }) -join ', '
	Write-Ok "$($tasks.Count) taken doorzocht (acties: $kinds)"
	$plan = $null
	if ($bootPwsh) {
		try { $plan = Get-TaskSwitchPlan $cfg $tasks } catch { & $fail "taken doorzoeken mislukt: $_" }
	} else {
		$found = @(Get-InstalledAppTasks $cfg $tasks)
		Write-Note "$($found.Count) taak/taken starten de geinstalleerde app; het omzetten wordt getoond zodra de losse PowerShell er is"
	}
	if ($plan) {
		$launcher = if ($plan.Records.Count -gt 0) { Get-BootLauncher $cfg } else { $null }
		for ($i = 0; $i -lt $plan.Records.Count; $i++) {
			$r = $plan.Records[$i]
			$label = "$($r.path)$($r.name)"
			if ($r.mode -ne 'repointed') { Write-Note "taak $label start de geinstalleerde app en wordt uitgezet"; continue }
			$info = Get-TaskDefinitionInfo $r.path $r.name
			Write-Ok "taak ${label}: start bij het opstarten, account $(Get-TaskAccountName $r.originalXml), aanmelding $($info.LogonTypeName), $(if ($info.Enabled) { 'aan' } else { 'uit' })"
			$doc = [xml]$r.originalXml
			$ns = New-TaskXmlNs $doc
			$n = 0
			foreach ($a in @($doc.SelectNodes('//t:Actions/*', $ns))) {
				$n++
				if ($a.LocalName -ne 'Exec') { Write-Note "  actie ${n}: $($a.LocalName) (blijft)"; continue }
				$cmd = $a.SelectSingleNode('t:Command', $ns).InnerText
				if (Test-InstalledAppExec $cfg $a $ns) {
					Write-Note "  actie ${n}: $cmd"
					Write-Note "     wordt: $($launcher.Command) $($launcher.Arguments)"
				} else { Write-Note "  actie ${n}: $cmd (blijft)" }
			}
			if ((Get-TaskXmlCore $cfg $plan.Ops[$i].xml) -ceq (Get-TaskXmlCore $cfg $r.originalXml)) { Write-Ok 'de rest van de taak blijft identiek (triggers, account, instellingen, andere acties)' }
			else { & $fail "taak ${label}: omzetten zou meer veranderen dan alleen de actie van de geinstalleerde app" }
			foreach ($p in @($launcher.Command, (Join-Path $cfg.ScriptsDir 'start-daily-ao.ps1'))) { if (-not (Test-Path -LiteralPath $p)) { & $fail "ontbreekt voor de opstart-taak: $p" } }
			Write-Note "  origineel wordt eerst bewaard in $($r.originalXmlFile)"
			if (Test-TaskWritableByMe $r.originalDacl) { Write-Ok 'je mag deze taak zelf aanpassen; geen UAC nodig' }
			else { Write-Note '  je mag deze taak alleen lezen: Windows vraagt een keer toestemming (UAC), en alleen als het opgeslagen wachtwoord niet hergebruikt wordt ook je wachtwoord' }
		}
		if ($plan.Ops.Count -gt 0) {
			$helper = Join-Path $PSScriptRoot 'daily-ao-task-helper.ps1'
			$pwshCmd = Get-Command pwsh -ErrorAction SilentlyContinue
			if (-not (Test-Path -LiteralPath $helper)) { & $fail "helper ontbreekt: $helper" }
			elseif (-not $pwshCmd) { & $fail 'pwsh niet gevonden; de helper kan niet starten' }
			else { Write-Ok "helper: $helper (met $($pwshCmd.Source))" }
		}
	}
	# Tasks an earlier, half-finished switch already repointed: they must still
	# be exactly that (the rollback restores them from the saved original).
	$planKeys = @(if ($plan) { $plan.Records | ForEach-Object { "$($_.path)|$($_.name)" } })
	foreach ($r in $prior) {
		if ($r['mode'] -ne 'repointed' -or $planKeys -contains "$($r['path'])|$($r['name'])") { continue }
		$label = "$($r['path'])$($r['name'])"
		$now = Get-TaskDefinitionInfo $r['path'] $r['name']
		if (-not $now) { & $fail "taak $label bestaat niet meer (de vorige overstap had hem omgezet)"; continue }
		$nowDoc = [xml]$now.Xml
		$nowNs = New-TaskXmlNs $nowDoc
		$bootCmd = (Get-BootLauncher $cfg).Command
		$repointed = @($nowDoc.SelectNodes('//t:Actions/t:Exec/t:Command', $nowNs) | Where-Object { Test-SamePath $_.InnerText $bootCmd }).Count -gt 0
		if ($repointed -and (Get-TaskXmlCore $cfg $now.Xml) -ceq (Get-TaskXmlCore $cfg $r['originalXml']) -and $now.Dacl -eq $r['originalDacl']) {
			Write-Ok "taak ${label}: al omgezet door de vorige run (start Ayo (daily) bij het opstarten; de rest identiek aan het origineel). Wordt overgeslagen."
			$file = $r['originalXmlFile']
			if ($file -and (Test-Path -LiteralPath $file) -and (Get-Content -Raw -Encoding unicode -LiteralPath $file) -ceq $r['originalXml']) { Write-Ok "  origineel bewaard (state en $file)" }
			else { Write-Note '  origineel bewaard in state.json (de rollback gebruikt dat)' }
		} else { & $fail "taak $label is na de vorige overstap veranderd; draai eerst rollback-to-installed-ao.ps1" }
	}
	if (-not ($plan -and $plan.Records.Count) -and $prior.Count -eq 0) { Write-Note 'geen actieve taak start de geinstalleerde app' }
	$frontendTask = Get-ComTask $cfg.FrontendTaskPath $cfg.FrontendTaskName
	if (-not $frontendTask) { Write-Ok "taak '$($cfg.FrontendTaskName)' wordt aangemaakt (je eigen taak, alleen op verzoek, geen admin nodig)" }
	elseif (Test-TaskWritableByMe $frontendTask.GetSecurityDescriptor(4)) { Write-Ok "taak '$($cfg.FrontendTaskName)' bestaat al (vorige run) en wordt bijgewerkt; je eigen taak, geen admin nodig" }
	else { & $fail "taak '$($cfg.FrontendTaskName)' bestaat al maar jij mag hem niet aanpassen; verwijder hem in Taakplanner" }
	$uac = $false
	if ($plan) { foreach ($r in $plan.Records) { $ti = Get-TaskDefinitionInfo $r.path $r.name; if ($ti -and -not (Test-TaskWritableByMe $ti.Dacl)) { $uac = $true } } }
	if ($uac) { Write-Note 'Windows-toestemming (UAC) nodig: ja, een keer' } else { Write-Ok 'Windows-toestemming (UAC) nodig: nee' }

	# --- Login items and shortcuts -------------------------------------------------
	Write-Step 'Aanmeld-items en snelkoppelingen'
	$runNames = @(if (Test-Path -LiteralPath $cfg.RunKey) { (Get-Item -LiteralPath $cfg.RunKey).GetValueNames() })
	$runValues = @(Get-InstalledRunValues $cfg)
	if ($runValues.Count) { foreach ($v in $runValues) { Write-Note "Run\$($v.name) start de geinstalleerde app en wordt apart gezet" } }
	else { Write-Ok "geen van de $($runNames.Count) Run-waarden start de geinstalleerde app" }
	$startupAll = @(if (Test-Path -LiteralPath $cfg.StartupDir) { Get-ChildItem -LiteralPath $cfg.StartupDir -Filter '*.lnk' -File })
	$startup = @(Get-InstalledAppLinks $cfg @($cfg.StartupDir))
	if ($startup.Count) { foreach ($l in $startup) { Write-Note "$($l.FullName) start de geinstalleerde app en wordt apart gezet" } }
	else { Write-Ok "geen van de $($startupAll.Count) snelkoppelingen in de Opstartmap start de geinstalleerde app" }
	$link = Get-AutostartLinkPath $cfg
	Write-Ok "autostart bij aanmelden: $link$(if (Test-Path -LiteralPath $link) { ' (bestaat al, wordt overschreven)' } else { ' (nieuw)' })"
	foreach ($l in @(Get-InstalledAppLinks $cfg $cfg.ShortcutDirs)) { Write-Note "snelkoppeling apart gezet: $($l.FullName)" }
	foreach ($dir in @($cfg.ShortcutDirs | Where-Object { $_ -and (Test-Path -LiteralPath $_) })) { Write-Note "nieuwe snelkoppeling: $(Join-Path $dir 'Ayo (daily).lnk')" }

	# --- Desktop credentials ------------------------------------------------------
	Write-Step 'Cloud-aanmelding en opgeslagen remotes'
	$sync = @(Get-CredentialSyncPlan $cfg)
	if ($sync.Count) { foreach ($c in $sync) { Write-Note "bij het starten: $($c.Label)" } }
	else { Write-Ok 'gelijk of afwezig; er wordt niets gekopieerd' }

	if ($problems.Count -gt 0) {
		Write-Step "Resultaat: $($problems.Count) probleem/problemen (LET OP hierboven); er is niets veranderd"
		return $false
	}
	if ($Preflight) { Write-Step 'Controle OK; nu overstappen' }
	else { Write-Step 'Resultaat: alles OK, de overstap kan. Deze controle heeft niets veranderd.' }
	return $true
}
