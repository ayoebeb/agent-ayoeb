<#
.SYNOPSIS
  Installs a portable PowerShell 7 under <AO home>\tools when `pwsh` is missing.

.DESCRIPTION
  The daily-AO scripts need PowerShell 7.4 or newer (`pwsh`). A fresh Windows PC
  only has Windows PowerShell 5.1, which cannot run them. This script is written
  to run in 5.1 (and 7): it downloads the official PowerShell zip from GitHub,
  checks its SHA-256 against the value GitHub publishes for that release, and
  unpacks it to <AO home>\tools\pwsh-<version>. Nothing is installed
  system-wide and no PATH is changed.

  Run:  powershell -NoProfile -ExecutionPolicy Bypass -File bootstrap-pwsh.ps1
  Then run the command it prints.
#>
[CmdletBinding()]
param([string]$AoHome = (Join-Path ([Environment]::GetFolderPath('UserProfile')) '.ao'))
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch { }

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$startScript = Join-Path $scriptDir 'start-daily-ao.ps1'
$tools = Join-Path $AoHome 'tools'

# PowerShell 7.4+ already there (on the PATH or portable)? Then just say so.
$have = $null
$cmd = Get-Command pwsh -ErrorAction SilentlyContinue
if ($cmd) {
	try { $v = [version](& $cmd.Source -NoProfile -Command '$PSVersionTable.PSVersion.ToString()' 2>$null).Split('-')[0]; if ($v -ge [version]'7.4') { $have = $cmd.Source } } catch { }
}
if (-not $have -and (Test-Path -LiteralPath $tools)) {
	$dirs = @(Get-ChildItem -LiteralPath $tools -Directory -Filter 'pwsh-*' -ErrorAction SilentlyContinue |
		Where-Object { $_.Name -match '^pwsh-\d+\.\d+\.\d+$' -and (Test-Path -LiteralPath (Join-Path $_.FullName 'pwsh.exe')) } |
		Sort-Object { [version]($_.Name.Substring(5)) } -Descending)
	if ($dirs.Count -gt 0) { $have = Join-Path $dirs[0].FullName 'pwsh.exe' }
}
if ($have) {
	Write-Host "PowerShell 7 is al aanwezig: $have"
	Write-Host "Ga verder met:  & `"$have`" -NoProfile -ExecutionPolicy Bypass -File `"$startScript`" -Prepare"
	return
}

Write-Host "PowerShell 7 ontbreekt; een losse kopie installeren in $tools (niet systeembreed)..."
$headers = @{ 'User-Agent' = 'ao-daily-bootstrap'; 'Accept' = 'application/vnd.github+json' }
$rel = Invoke-RestMethod -Uri 'https://api.github.com/repos/PowerShell/PowerShell/releases/latest' -Headers $headers -TimeoutSec 60
if ($rel.tag_name -notmatch '^v(\d+\.\d+\.\d+)$') { throw "Onverwachte PowerShell-release: $($rel.tag_name)" }
$version = $Matches[1]
$file = "PowerShell-$version-win-x64.zip"
$asset = $rel.assets | Where-Object { $_.name -eq $file } | Select-Object -First 1
if (-not $asset) { throw "De release v$version heeft geen $file" }
$want = $null
if ($asset.PSObject.Properties['digest'] -and "$($asset.digest)" -match '^sha256:([0-9a-fA-F]{64})$') { $want = $Matches[1] }
if (-not $want) {
	$h = $rel.assets | Where-Object { $_.name -eq 'hashes.sha256' } | Select-Object -First 1
	if ($h) {
		$text = [string](Invoke-WebRequest -Uri $h.browser_download_url -Headers $headers -UseBasicParsing -TimeoutSec 60).Content
		$line = $text -split "`n" | Where-Object { $_ -match [regex]::Escape($file) } | Select-Object -First 1
		if ($line -and $line -match '([0-9a-fA-F]{64})') { $want = $Matches[1] }
	}
}
if (-not $want) { throw "GitHub publiceert geen SHA-256 voor $file; er wordt niets geinstalleerd." }

New-Item -ItemType Directory -Force -Path $tools | Out-Null
# Windows PowerShell 5.1's Expand-Archive only accepts a file name ending in .zip.
$zip = Join-Path $tools "download-$file"
if (Test-Path -LiteralPath $zip) { Remove-Item -LiteralPath $zip -Force }
Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $zip -UseBasicParsing -TimeoutSec 900
$got = (Get-FileHash -Algorithm SHA256 -LiteralPath $zip).Hash
if ($got -ne $want.ToUpperInvariant()) {
	Remove-Item -LiteralPath $zip -Force
	throw "Checksum komt niet overeen voor $file (verwacht $want, gekregen $got). De download is verwijderd."
}
$final = Join-Path $tools "pwsh-$version"
$staging = "$final.partial"
if (Test-Path -LiteralPath $staging) { Remove-Item -LiteralPath $staging -Recurse -Force }
Expand-Archive -LiteralPath $zip -DestinationPath $staging
if (Test-Path -LiteralPath $final) { Remove-Item -LiteralPath $final -Recurse -Force }
Move-Item -LiteralPath $staging -Destination $final
Remove-Item -LiteralPath $zip -Force
$exe = Join-Path $final 'pwsh.exe'
Write-Host "PowerShell $version geinstalleerd: $exe (checksum gecontroleerd)"
Write-Host "Ga verder met:  & `"$exe`" -NoProfile -ExecutionPolicy Bypass -File `"$startScript`" -Prepare"
