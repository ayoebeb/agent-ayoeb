<#
.SYNOPSIS
  Changes the scheduled tasks that start the installed app. Started by the
  daily scripts as administrator (one UAC prompt); not meant to be run by hand.

.DESCRIPTION
  Reads the request file the calling script wrote (the task definitions to
  register, or tasks to enable/disable), applies it and writes
  <request>.result.json. A task that runs with a stored password is first
  changed without asking for it; only if Windows refuses is the password of
  the task's account asked, once, in this window. It is handed to Windows and
  not kept anywhere else.
#>
[CmdletBinding()]
param([Parameter(Mandatory)][string]$Request)
. (Join-Path $PSScriptRoot 'daily-ao-common.ps1')

try { $Host.UI.RawUI.WindowTitle = 'Ayo (daily) - geplande taak aanpassen' } catch { }
$resultFile = "$Request.result.json"
$out = [ordered]@{ ok = $false; usedPassword = $false; done = @(); error = $null }
try {
	$req = Get-Content -Raw -LiteralPath $Request | ConvertFrom-Json -AsHashtable
	Write-Host 'Ayo (daily) past je geplande taak aan:' -ForegroundColor Cyan
	foreach ($line in @($req.purpose)) { Write-Host "  - $line" }
	$r = Invoke-TaskOpsNow @($req.ops) -AllowPasswordPrompt
	$out.ok = $true
	$out.usedPassword = $r.UsedPassword
	$out.done = $r.Done
	Write-Host 'Klaar. Dit venster sluit vanzelf.' -ForegroundColor Green
	Start-Sleep -Seconds 2
} catch {
	$out.error = "$_"
	Write-Host "Niet gelukt: $_" -ForegroundColor Red
	Write-Host 'Het script in je andere venster meldt wat er wel en niet is veranderd. Dit venster sluit over 10 seconden.'
	Start-Sleep -Seconds 10
}
$out | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $resultFile -Encoding utf8
if (-not $out.ok) { exit 1 }
