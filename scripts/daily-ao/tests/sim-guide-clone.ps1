<#
.SYNOPSIS
  Runs the copy-paste step 2 of tweede-pc-handleiding.nl.md (store the token, clone)
  exactly as written, in Windows PowerShell 5.1 and in PowerShell 7, against a fake
  GitHub on a high port, and then lets the scripts take over (Prepare's GitHub step,
  Initialize-Checkout). Only the host, scheme and clone folder are substituted.
  Touches nothing outside %TEMP%\ao-sim-guide.

  Run:  pwsh -NoProfile -File scripts\daily-ao\tests\sim-guide-clone.ps1
#>
[CmdletBinding()]
param([int]$Port = 3683, [switch]$Keep)
$ErrorActionPreference = 'Stop'
$scriptsDir = Split-Path $PSScriptRoot -Parent
. (Join-Path $PSScriptRoot 'sim-common.ps1')

$ro = 'github_pat_SIMULATION_READONLY_0123456789abcdef'
$rw = 'github_pat_SIMULATION_READWRITE_0123456789abcd'
$other = 'github_pat_SIMULATION_OTHERACCOUNT_0123456789ab'
$serverPid = $null
$results = @()

# The snippet, straight from the guide.
$guide = Get-Content -Raw -LiteralPath (Join-Path $scriptsDir 'tweede-pc-handleiding.nl.md')
if ($guide -notmatch '(?s)\*\*2\. Token opslaan en clonen\*\*.*?```powershell\r?\n(?<code>.*?)```') { throw 'step 2 snippet not found in the guide' }
$snippet = $Matches['code']

foreach ($shell in @(@{ Name = 'Windows PowerShell 5.1'; Exe = 'powershell.exe' }, @{ Name = 'PowerShell 7'; Exe = 'pwsh.exe' })) {
	$root = New-SimRoot 'guide'
	$env:GCM_CREDENTIAL_STORE = 'plaintext'
	$env:GCM_PLAINTEXT_STORE_PATH = Join-Path $root 'aohome\gcm-store'
	$env:GIT_CONFIG_GLOBAL = Join-Path $root 'gitconfig'
	# The work PC: gh is set up as the credential helper and the WORK account is active.
	"[credential `"http://127.0.0.1:$Port`"]`n`thelper = `"!f() { echo username=workuser; echo password=$other; }; f`"`n" | Set-Content -LiteralPath $env:GIT_CONFIG_GLOBAL
	try {
		Write-SimStep "Guide step 2 in $($shell.Name)"
		$seed = Join-Path $root 'seed'
		git init -q -b main $seed
		git -C $seed -c user.name=sim -c user.email=sim@example.invalid commit -q --allow-empty -m seed
		git clone -q --bare $seed (Join-Path $root 'srv\agent-ayoeb.git')
		$p = Start-Process node -ArgumentList @((Join-Path $PSScriptRoot 'fake-github.mjs'), "$Port", (Join-Path $root 'srv\agent-ayoeb.git'), 'ayoebeb/agent-ayoeb', $ro, $rw, $other, (Join-Path $root 'requests.log')) `
			-PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $root 'srv.out') -RedirectStandardError (Join-Path $root 'srv.err')
		$serverPid = $p.Id
		for ($i = 0; $i -lt 50 -and -not ((Test-Path (Join-Path $root 'srv.out')) -and ((Get-Content (Join-Path $root 'srv.out') -Raw) -match 'listening')); $i++) { Start-Sleep -Milliseconds 200 }

		$checkout = Join-Path $root 'aohome\daily\agent-ayoeb'
		# Only the world is substituted (host, scheme, folder), never the logic.
		$text = $snippet.Replace('protocol=https', 'protocol=http').Replace('host=github.com', "host=127.0.0.1:$Port").Replace('https://github.com/ayoebeb', "http://127.0.0.1:$Port/ayoebeb").Replace('"$HOME\.ao\daily\agent-ayoeb"', "`"$checkout`"")
		# The one line that cannot run unattended: Read-Host -AsSecureString needs a real
		# console (it blocks on piped input in 5.1). It is replaced by the same SecureString.
		$prompt = $text -split "`n" | Where-Object { $_ -match 'Read-Host' } | Select-Object -First 1
		if (-not $prompt) { throw 'the guide snippet has no Read-Host line' }
		$text = $text.Replace($prompt, "`$t = ConvertTo-SecureString '$ro' -AsPlainText -Force")
		$file = Join-Path $root 'guide-step2.ps1'
		Set-Content -LiteralPath $file -Value $text -Encoding utf8
		$out = & $shell.Exe -NoProfile -ExecutionPolicy Bypass -File $file 2>&1 | Out-String
		$code = $LASTEXITCODE
		Assert-Sim ($code -eq 0 -and (Test-Path -LiteralPath (Join-Path $checkout '.git'))) "$($shell.Name): the snippet stored the token and cloned the private repo (exit $code)"
		if ($code -ne 0) { Write-Host $out }
		Assert-Sim ($out -notmatch [regex]::Escape($ro)) "$($shell.Name): the token was not printed"
		$stored = @(Get-ChildItem -LiteralPath $env:GCM_PLAINTEXT_STORE_PATH -Recurse -File -ErrorAction SilentlyContinue | Where-Object { $_.FullName -like '*\agent-ayoeb.git\*' })
		Assert-Sim ($stored.Count -eq 1) "$($shell.Name): one credential, keyed by the repository path"

		# Now the scripts take over.
		$cfgFile = New-SimConfig $root $Port "http://127.0.0.1:$Port/ayoebeb/agent-ayoeb.git"
		$check = & pwsh -NoProfile -Command {
			param($scriptsDir, $cfgFile)
			. (Join-Path $scriptsDir 'daily-ao-common.ps1')
			$cfg = Get-DailyConfig -ConfigFile $cfgFile
			Invoke-GitHubAccessStep $cfg
			Initialize-Checkout $cfg
			Invoke-Git $cfg @('fetch', '--quiet', 'origin', 'main') | Out-Null
			'done'
		} -args $scriptsDir, $cfgFile 2>&1 | Out-String
		Assert-Sim ($check -match 'alleen-lezen token werkt' -and $check -match 'done') "$($shell.Name): Prepare's GitHub step accepts it and the checkout fetches with the work account active"
		$local = (& git -C $checkout config --local --list) -join "`n"
		Assert-Sim ($local -match 'usehttppath=true' -and $local -notmatch 'github_pat') "$($shell.Name): the checkout now carries the per-URL settings and no secret"
	} finally {
		if ($serverPid) { Stop-Process -Id $serverPid -Force -ErrorAction SilentlyContinue; $serverPid = $null }
		if (-not $Keep) { Start-Sleep -Milliseconds 800; Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
	}
}
$fails = Write-SimSummary
exit $(if ($fails -eq 0) { 0 } else { 1 })
