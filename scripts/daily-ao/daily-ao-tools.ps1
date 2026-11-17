# daily-ao-tools.ps1
# Toolchain helpers for the daily AO: Git detection, a portable Node, and the
# download + checksum helper. Dot-sourced by daily-ao-common.ps1; not run
# directly. Everything is installed under <AO home>\tools, never system-wide.
#Requires -Version 7.4

# ---------------------------------------------------------------------------
# Downloads
# ---------------------------------------------------------------------------
function Get-FileSha256([string]$Path) { (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash }

# Downloads Uri to OutFile and checks its SHA-256 against the published value. A
# file that does not match is deleted and the call throws: nothing unverified is
# ever left behind or used.
function Get-VerifiedDownload {
	param([Parameter(Mandatory)][string]$Uri, [Parameter(Mandatory)][string]$OutFile, [Parameter(Mandatory)][string]$Sha256, [int]$TimeoutSec = 900)
	if ($Sha256 -notmatch '^[0-9a-fA-F]{64}$') { throw "Geen geldige SHA-256 voor $Uri" }
	$part = "$OutFile.part"
	Remove-Item -LiteralPath $part, $OutFile -Force -ErrorAction SilentlyContinue
	Invoke-WebRequest -Uri $Uri -OutFile $part -TimeoutSec $TimeoutSec
	$got = Get-FileSha256 $part
	if ($got -ine $Sha256) {
		Remove-Item -LiteralPath $part -Force -ErrorAction SilentlyContinue
		throw "Checksum komt niet overeen voor $(Split-Path $Uri -Leaf): verwacht $($Sha256.ToLowerInvariant()), gekregen $($got.ToLowerInvariant()). De download is verwijderd."
	}
	Move-Item -LiteralPath $part -Destination $OutFile -Force
}

# ---------------------------------------------------------------------------
# Git
# ---------------------------------------------------------------------------
# A process that started before Git was installed (an agent, a service) does not
# have it on its PATH yet. Look in the usual per-machine and per-user places.
function Add-GitToPathIfInstalled {
	if (Get-Command git -CommandType Application -ErrorAction SilentlyContinue) { return $true }
	$roots = @($env:ProgramFiles, ${env:ProgramFiles(x86)}, (Join-Path $env:LOCALAPPDATA 'Programs')) | Where-Object { $_ }
	foreach ($root in $roots) {
		$dir = Join-Path $root 'Git\cmd'
		if (Test-Path -LiteralPath (Join-Path $dir 'git.exe')) { $env:Path = "$dir;$env:Path"; return $true }
	}
	return $false
}

# Git is the one tool this setup does not install: it needs a real install (and
# brings Git Credential Manager). A clear message, then stop.
function Assert-GitAvailable {
	if (-not (Add-GitToPathIfInstalled)) {
		throw ("Git ontbreekt op deze pc. Installeer Git for Windows (https://git-scm.com/download/win, of in PowerShell: winget install --id Git.Git -e), " +
			'sluit daarna dit venster en probeer het opnieuw in een nieuw venster.')
	}
	$text = [string](& git --version 2>&1)
	if ($text -match 'git version (\d+)\.(\d+)') {
		if ([int]$Matches[1] -lt 2 -or ([int]$Matches[1] -eq 2 -and [int]$Matches[2] -lt 30)) { throw "Git is te oud ($text). Installeer een nieuwere Git for Windows (2.30 of nieuwer)." }
	} else { throw "Git gaf geen versie terug ($text). Installeer Git for Windows opnieuw." }
}

# Git Credential Manager ships with Git for Windows (unless the installer option
# was unticked). `manager` is the helper name Git for Windows configures.
function Test-GitCredentialManager {
	try { & git credential-manager --version *> $null; return ($LASTEXITCODE -eq 0) } catch { return $false }
}

# ---------------------------------------------------------------------------
# Node: what CI uses (NodeMajor). Order: fnm install, portable copy under
# <AO home>\tools, Node on the PATH. A PATH Node of another major (22 or newer)
# is accepted but only as a last resort; if nothing exact is found a portable
# Node is installed.
# ---------------------------------------------------------------------------
function Get-PortableNodeDirs($cfg) {
	@(Get-ChildItem -LiteralPath $cfg.ToolsDir -Directory -Filter 'node-v*-win-x64' -ErrorAction SilentlyContinue |
		Where-Object { $_.Name -match '^node-v\d+\.\d+\.\d+-win-x64$' -and (Test-Path -LiteralPath (Join-Path $_.FullName 'node.exe')) } |
		Sort-Object { [version]($_.Name -replace '^node-v|-win-x64$', '') } -Descending)
}

# The best Node found, or $null. Changes nothing. Exact = the major CI uses.
function Get-NodeCandidate($cfg) {
	$major = $script:DailyDefaults.NodeMajor
	if ($cfg.FnmRoot -and (Test-Path -LiteralPath $cfg.FnmRoot)) {
		$best = Get-ChildItem -LiteralPath $cfg.FnmRoot -Directory |
			Where-Object { $_.Name -match "^v$major\.\d+\.\d+$" } |
			Sort-Object { [version]($_.Name.TrimStart('v')) } -Descending | Select-Object -First 1
		if ($best) {
			$dir = Join-Path $best.FullName 'installation'
			if (Test-Path -LiteralPath (Join-Path $dir 'node.exe')) { return [pscustomobject]@{ Dir = $dir; Major = $major; Exact = $true; Source = 'fnm' } }
		}
	}
	$portable = @(Get-PortableNodeDirs $cfg | Where-Object { $_.Name -match "^node-v$major\." }) | Select-Object -First 1
	if ($portable) { return [pscustomobject]@{ Dir = $portable.FullName; Major = $major; Exact = $true; Source = 'portable' } }
	$node = Get-Command node -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
	if ($node) {
		$pathMajor = 0
		try { $pathMajor = [int]((& $node.Source -v).TrimStart('v').Split('.')[0]) } catch { }
		if ($pathMajor -eq $major) { return [pscustomobject]@{ Dir = (Split-Path $node.Source -Parent); Major = $pathMajor; Exact = $true; Source = 'path' } }
		if ($pathMajor -ge 22) { return [pscustomobject]@{ Dir = (Split-Path $node.Source -Parent); Major = $pathMajor; Exact = $false; Source = 'path' } }
	}
	return $null
}

# Folder with a usable node.exe. Never installs anything (the read-only switch
# check and the boot start use it); throws when there is none.
function Get-NodeDir($cfg) {
	$c = Get-NodeCandidate $cfg
	if (-not $c) { throw "Node.js $($script:DailyDefaults.NodeMajor) niet gevonden. start-daily-ao.ps1 -Prepare installeert een losse Node onder $($cfg.ToolsDir) (niet systeembreed)." }
	if (-not $c.Exact) { Write-Warn2 "Node $($c.Major) in gebruik; CI gebruikt Node $($script:DailyDefaults.NodeMajor)." }
	return $c.Dir
}

function Get-NodeLatestRelease([int]$Major) {
	$index = Invoke-RestMethod -Uri 'https://nodejs.org/dist/index.json' -TimeoutSec 60
	$best = @($index | Where-Object { $_.version -match "^v$Major\.\d+\.\d+$" -and @($_.files) -contains 'win-x64-zip' }) |
		Sort-Object { [version]($_.version.TrimStart('v')) } -Descending | Select-Object -First 1
	if (-not $best) { throw "nodejs.org kent geen Node $Major voor Windows x64" }
	return [string]$best.version
}

# nodejs.org publishes SHASUMS256.txt next to every release.
function Get-NodeExpectedSha256([string]$Version, [string]$File) {
	$text = [string](Invoke-WebRequest -Uri "https://nodejs.org/dist/$Version/SHASUMS256.txt" -TimeoutSec 60).Content
	foreach ($line in ($text -split "`n")) {
		if ($line.Trim() -match '^([0-9a-fA-F]{64})\s+\*?(\S+)$' -and $Matches[2] -eq $File) { return $Matches[1] }
	}
	throw "SHASUMS256.txt van Node $Version noemt geen checksum voor $File"
}

# Exact Node found or installed. Source of the zip: an already downloaded copy
# (config nodeZipCache, used by the simulation) or nodejs.org; either way the
# zip's SHA-256 is checked against nodejs.org's published checksum.
function Install-NodeToolchain($cfg) {
	$have = Get-NodeCandidate $cfg
	if ($have -and $have.Exact) { return $have.Dir }
	$major = $script:DailyDefaults.NodeMajor
	Write-Step "Node $major installeren in $($cfg.ToolsDir) (alleen voor AO, niet systeembreed)"
	try {
		$version = Get-NodeLatestRelease $major
		$name = "node-$version-win-x64"
		$file = "$name.zip"
		$want = Get-NodeExpectedSha256 $version $file
		$final = Join-Path $cfg.ToolsDir $name
		$zip = $null
		if ($cfg.NodeZipCache -and (Test-Path -LiteralPath $cfg.NodeZipCache) -and (Get-FileSha256 $cfg.NodeZipCache) -ieq $want) {
			$zip = $cfg.NodeZipCache; Write-Note "gecontroleerde lokale kopie gebruikt: $zip"
		} else {
			$zip = Join-Path $cfg.ToolsDir $file
			Get-VerifiedDownload -Uri "https://nodejs.org/dist/$version/$file" -OutFile $zip -Sha256 $want
		}
		$staging = "$final.partial"
		if (Test-Path -LiteralPath $staging) { Remove-Item -LiteralPath $staging -Recurse -Force }
		Expand-Archive -LiteralPath $zip -DestinationPath $staging
		$inner = Join-Path $staging $name
		if (-not (Test-Path -LiteralPath (Join-Path $inner 'node.exe'))) { throw "$file bevat geen $name\node.exe" }
		if (Test-Path -LiteralPath $final) { Remove-Item -LiteralPath $final -Recurse -Force }
		Move-Item -LiteralPath $inner -Destination $final
		Remove-Item -LiteralPath $staging -Recurse -Force -ErrorAction SilentlyContinue
		if (Test-SamePath (Split-Path $zip -Parent) $cfg.ToolsDir) { Remove-Item -LiteralPath $zip -Force }
		$got = [string](& (Join-Path $final 'node.exe') -v)
		if ($got.Trim() -ne $version) { throw "Node $version geinstalleerd, maar node.exe meldt $got" }
		return $final
	} catch {
		if ($have) { Write-Warn2 "Node $major installeren mislukte ($_); verder met Node $($have.Major) uit $($have.Dir)."; return $have.Dir }
		throw "Node $major is niet aanwezig en kon niet worden geinstalleerd: $_"
	}
}

# Where an installed Orchestrator.inc lives. The per-user installer uses
# %LOCALAPPDATA%\Programs\agent-orchestrator; otherwise the uninstall entries
# (per user or per machine) name the folder. Falls back to the default path.
function Find-InstalledAppDir {
	$default = Join-Path $env:LOCALAPPDATA 'Programs\agent-orchestrator'
	if (Test-Path -LiteralPath (Join-Path $default 'agent-orchestrator.exe')) { return $default }
	$keys = @('HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall', 'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall',
		'HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall')
	foreach ($k in $keys) {
		if (-not (Test-Path -LiteralPath $k)) { continue }
		foreach ($sub in Get-ChildItem -LiteralPath $k -ErrorAction SilentlyContinue) {
			$loc = $sub.GetValue('InstallLocation')
			if ($loc -and (Test-Path -LiteralPath (Join-Path ([string]$loc).Trim('"') 'agent-orchestrator.exe'))) { return ([string]$loc).Trim('"').TrimEnd('\') }
		}
	}
	return $default
}
