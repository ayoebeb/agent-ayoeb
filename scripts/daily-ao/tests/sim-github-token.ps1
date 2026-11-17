<#
.SYNOPSIS
  Simulation of the GitHub-token flow (read-only token per repository URL),
  against a fake GitHub on a high port. Touches nothing outside %TEMP%\ao-sim-github.

.DESCRIPTION
  Proves, with real git and real Git Credential Manager (plaintext store inside the
  scratch folder, never the real Windows Credential Manager):
    - the wrong-account failure: a global `gh`-style helper answers with another
      account's token, and the private repository is "not found";
    - a wrong / other-account / read-write / classic token is refused and nothing
      is stored;
    - a read-only token is stored for exactly that repository URL, and the daily
      checkout then clones and fetches with it even while the global helper hands
      out the wrong account (the `gh auth switch` case);
    - the token reaches no file, log or config, and no other repository's login;
    - an expired token is erased by git, reported with the fix, and replaced.

  Run:  pwsh -NoProfile -File scripts\daily-ao\tests\sim-github-token.ps1
#>
[CmdletBinding()]
param([int]$Port = 3681, [switch]$Keep)
$ErrorActionPreference = 'Stop'
$scriptsDir = Split-Path $PSScriptRoot -Parent
. (Join-Path $PSScriptRoot 'sim-common.ps1')

$root = New-SimRoot 'github'
$ro = 'github_pat_SIMULATION_READONLY_0123456789abcdef'
$rw = 'github_pat_SIMULATION_READWRITE_0123456789abcd'
$other = 'github_pat_SIMULATION_OTHERACCOUNT_0123456789ab'
$ro2 = 'github_pat_SIMULATION_READONLY_SECOND_0123456789'
$repoUrl = "http://127.0.0.1:$Port/ayoebeb/agent-ayoeb.git"
$script:serverPid = $null
$transcript = Join-Path $root 'transcript.log'

# Stand-ins for the machine's state. The credential store and the global git
# config are the simulation's own.
$env:GCM_CREDENTIAL_STORE = 'plaintext'
$env:GCM_PLAINTEXT_STORE_PATH = Join-Path $root 'aohome\gcm-store'
$env:GIT_CONFIG_GLOBAL = Join-Path $root 'gitconfig'
"[include]`n`tpath = $(($root -replace '\\', '/'))/gh-helper.config`n" | Set-Content -LiteralPath $env:GIT_CONFIG_GLOBAL

function Set-GlobalGhHelper([string]$Token, [string]$User) {
	# What `gh auth setup-git` configures: a helper for the host that answers with
	# the ACTIVE gh account's token.
	# Kept in an INCLUDED file so the main global config is only ever changed by someone
	# other than the test (Git Credential Manager's own auto-detection, or a bug of ours).
	"[credential `"http://127.0.0.1:$Port`"]`n`thelper = `"!f() { echo username=$User; echo password=$Token; }; f`"`n" | Set-Content -LiteralPath (Join-Path $root 'gh-helper.config')
}

function Start-FakeGitHub([string]$ReadToken) {
	if ($script:serverPid) { Stop-Process -Id $script:serverPid -Force -ErrorAction SilentlyContinue; Start-Sleep -Milliseconds 300 }
	Remove-Item -LiteralPath (Join-Path $root 'srv.out') -Force -ErrorAction SilentlyContinue
	$p = Start-Process node -ArgumentList @((Join-Path $PSScriptRoot 'fake-github.mjs'), "$Port", (Join-Path $root 'srv\agent-ayoeb.git'), 'ayoebeb/agent-ayoeb', $ReadToken, $rw, $other, (Join-Path $root 'requests.log')) `
		-PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $root 'srv.out') -RedirectStandardError (Join-Path $root 'srv.err')
	$script:serverPid = $p.Id
	for ($i = 0; $i -lt 50; $i++) {
		if ((Test-Path (Join-Path $root 'srv.out')) -and ((Get-Content (Join-Path $root 'srv.out') -Raw) -match 'listening')) { break }
		Start-Sleep -Milliseconds 200
	}
}

# What a plain `git ls-remote` does with the machine's own helpers. The scripts never
# do this (git would tell every helper to store/erase the result); the test uses it to
# show the problem and to build the "other logins already on this PC" state.
function Test-AmbientAccess {
	$ref = Get-GitRepoRef $cfg
	$r = Invoke-GitText $cfg @('ls-remote', '--heads', $ref.Url, 'refs/heads/main')
	[pscustomobject]@{ Ok = ($r.ExitCode -eq 0 -and $r.Output -match '(?m)^[0-9a-f]{40}\s'); Kind = (Get-GitFailureKind $r.Output) }
}
# Everything the PC's own logins consist of: the global git config and every
# credential-store entry that is NOT keyed by our repository path.
function Get-MachineLoginState {
	$parts = @("gitconfig:$((Get-FileHash -LiteralPath $env:GIT_CONFIG_GLOBAL -Algorithm SHA256).Hash)")
	foreach ($f in @(Get-ChildItem -LiteralPath $env:GCM_PLAINTEXT_STORE_PATH -Recurse -File -ErrorAction SilentlyContinue | Where-Object { $_.FullName -notlike '*\agent-ayoeb.git\*' } | Sort-Object FullName)) {
		$parts += "$($f.FullName.Substring($root.Length)):$((Get-FileHash -LiteralPath $f.FullName -Algorithm SHA256).Hash)"
	}
	return ($parts -join '|')
}
function ConvertTo-Secure([string]$Text) { ConvertTo-SecureString $Text -AsPlainText -Force }

# Credentials keyed by the repository path: the per-URL token. (Git also tells GCM to
# cache whatever login a `gh` helper just handed out, host-wide, like on any PC; that
# is machine state, not ours, so it is handled separately.)
function Get-StoredCredentialFiles {
	@(Get-ChildItem -LiteralPath $env:GCM_PLAINTEXT_STORE_PATH -Recurse -File -ErrorAction SilentlyContinue | Where-Object { $_.FullName -like '*\agent-ayoeb.git\*' })
}
function Test-Throws([scriptblock]$Block, [string]$Pattern) {
	try { & $Block | Out-Null; return $false } catch { return ("$_" -match $Pattern) }
}

try {
	Start-Transcript -LiteralPath $transcript | Out-Null
	Write-Host "Scratch folder: $root  (fake GitHub on 127.0.0.1:$Port)"
	$promptBefore = "$($env:GIT_TERMINAL_PROMPT)|$($env:GCM_INTERACTIVE)"

	# --- Fake GitHub with a one-commit repository ------------------------------------
	$seed = Join-Path $root 'seed'
	git init -q -b main $seed
	git -C $seed -c user.name=sim -c user.email=sim@example.invalid commit -q --allow-empty -m seed
	git clone -q --bare $seed (Join-Path $root 'srv\agent-ayoeb.git')
	Start-FakeGitHub $ro
	$cfgFile = New-SimConfig $root $Port $repoUrl
	. (Join-Path $scriptsDir 'daily-ao-common.ps1')
	$cfg = Get-DailyConfig -ConfigFile $cfgFile
	Initialize-DailyDirs $cfg
	Assert-GitAvailable

	Write-SimStep '1. The wrong-account failure (the problem this solves)'
	Set-GlobalGhHelper $other 'workuser'
	$amb = Test-AmbientAccess
	Assert-Sim (-not $amb.Ok -and $amb.Kind -eq 'auth') "with the work account active in gh, the private repo is not readable (kind=$($amb.Kind))"
	Assert-Sim (-not (Test-DailyGitHubTokenStored $cfg)) 'no token is stored yet'
	Set-GlobalGhHelper $rw 'ayoebeb'
	Assert-Sim ((Test-AmbientAccess).Ok) 'with the ayoebeb account active in gh it works (this is what breaks after `gh auth switch`)'
	Set-GlobalGhHelper $other 'workuser'
	# The PC already holds other logins (the work account's, "Yoobi"): a host-wide entry
	# in Credential Manager and the gh-style helper in the global git config. Snapshot
	# them now; the whole token flow below must leave them byte-for-byte alone.
	[void](Invoke-GitText $cfg @('-c', 'credential.helper=', '-c', 'credential.helper=manager', 'credential', 'approve') -Stdin "protocol=http`nhost=127.0.0.1:$Port`nusername=yoobiuser`npassword=YOOBI_LOGIN_MUST_STAY`n`n")
	$loginsBefore = Get-MachineLoginState

	Write-SimStep '2. Wrong tokens are refused and nothing is stored'
	Assert-Sim (Test-Throws { Set-DailyGitHubToken $cfg (ConvertTo-Secure 'ghp_SIM_NOT_A_REAL_CLASSIC_TOKEN') } 'fine-grained') 'a classic token is refused'
	Assert-Sim (Test-Throws { Set-DailyGitHubToken $cfg (ConvertTo-Secure 'github_pat_SIMULATION_UNKNOWN_0123456789abcdefgh') } 'geen toegang') 'an unknown token is refused'
	Assert-Sim (Test-Throws { Set-DailyGitHubToken $cfg (ConvertTo-Secure $other) } 'geen toegang') "another account's token (no access: 404) is refused"
	Assert-Sim (Test-Throws { Set-DailyGitHubToken $cfg (ConvertTo-Secure $rw) } 'SCHRIJVEN') 'a token that can WRITE is refused'
	Assert-Sim (@(Get-StoredCredentialFiles).Count -eq 0) 'nothing was stored by any of the refused tokens'

	Write-SimStep '3. A read-only token is stored for exactly this URL'
	Set-DailyGitHubToken $cfg (ConvertTo-Secure $ro)
	$files = @(Get-StoredCredentialFiles)
	$firstKey = if ($files.Count -gt 0) { $files[0].FullName.Substring($root.Length) } else { '(none)' }
	Assert-Sim ($files.Count -eq 1) 'exactly one per-URL credential was stored'
	Assert-Sim ($firstKey -like '*127.0.0.1-*\ayoebeb\agent-ayoeb.git\*') "its key includes the repository path ($firstKey)"
	Assert-Sim (Test-DailyGitHubTokenStored $cfg) 'Test-DailyGitHubTokenStored says yes'
	Assert-Sim ((Test-DailyGitHubAccess $cfg 'stored').Ok) 'the stored token reads the repository on its own'
	# Another repository on the same host must not get this token, nor a host-only lookup.
	$otherRepo = Invoke-GitText $cfg @('credential', 'fill') -Stdin "protocol=http`nhost=127.0.0.1:$Port`npath=workorg/other.git`n`n"
	Assert-Sim ($otherRepo.Output -notmatch [regex]::Escape($ro)) 'a different repository on the same host does not receive the token'
	$hostOnly = Invoke-GitText $cfg @('credential', 'fill') -Stdin "protocol=http`nhost=127.0.0.1:$Port`n`n"
	Assert-Sim ($hostOnly.Output -notmatch [regex]::Escape($ro)) 'a host-only lookup does not receive the token'

	Write-SimStep '4. Clone and fetch with the token while the WRONG gh account is active'
	Initialize-Checkout $cfg
	Assert-Sim (Test-Path -LiteralPath (Join-Path $cfg.Checkout '.git')) 'the daily checkout was cloned (private repo, work account active in gh)'
	$local = (& git -C $cfg.Checkout config --local --list) -join "`n"
	Assert-Sim ($local -match 'credential\.http://127\.0\.0\.1:\d+/ayoebeb/agent-ayoeb\.git\.usehttppath=true') 'the checkout has the per-URL credential settings (useHttpPath)'
	Assert-Sim ($local -match 'credential\.http://127\.0\.0\.1:\d+/ayoebeb/agent-ayoeb\.git\.helper=manager') 'the per-URL helper is Git Credential Manager only'
	Assert-Sim ($local -notmatch 'password|token|github_pat') 'no secret in the checkout config'
	git -C $seed -c user.name=sim -c user.email=sim@example.invalid commit -q --allow-empty -m 'second commit'
	git -C $seed push -q (Join-Path $root 'srv\agent-ayoeb.git') main
	Set-GlobalGhHelper $other 'workuser'   # `gh auth switch` to the work account
	Invoke-Git $cfg @('fetch', '--quiet', 'origin', 'main') | Out-Null
	$remoteHead = ([string](& git -C $cfg.Checkout rev-parse origin/main)).Trim()
	$seedHead = ([string](& git -C $seed rev-parse HEAD)).Trim()
	Assert-Sim ($remoteHead -eq $seedHead) 'fetch picked up a new commit with the work account active'
	Set-GlobalGhHelper $rw 'ayoebeb'       # and switched back
	Invoke-Git $cfg @('fetch', '--quiet', 'origin', 'main') | Out-Null
	Assert-Sim $true 'fetch still works after switching back'
	$requests = Get-Content (Join-Path $root 'requests.log')
	Assert-Sim (@($requests | Where-Object { $_ -match 'token=read' }).Count -gt 0) 'the server saw the read-only token in use'
	Assert-Sim ("$($env:GIT_TERMINAL_PROMPT)|$($env:GCM_INTERACTIVE)" -eq $promptBefore) 'the no-prompt settings were restored after each call (agents the daemon starts must not inherit them)'

	Write-SimStep '5. The Prepare step with a working token'
	Set-GlobalGhHelper $other 'workuser'
	Invoke-GitHubAccessStep $cfg
	Assert-Sim $true 'Invoke-GitHubAccessStep accepts the stored token without asking'

	Write-SimStep '5b. A token stored by hand that CAN write is caught by Prepare'
	$ref = Get-GitRepoRef $cfg
	$approveArgs = @(ConvertTo-GitConfigArgs (Get-DailyCredentialSettings $cfg)) + @('credential', 'approve')
	[void](Invoke-GitText $cfg $approveArgs -Stdin "protocol=$($ref.Scheme)`nhost=$($ref.Host)`npath=$($ref.Path)`nusername=$($ref.Owner)`npassword=$rw`n`n")
	Assert-Sim (Test-Throws { Invoke-GitHubAccessStep $cfg } 'SCHRIJVEN') 'Prepare refuses an already-stored token that can write'
	Set-DailyGitHubToken $cfg (ConvertTo-Secure $ro)
	Invoke-GitHubAccessStep $cfg
	Assert-Sim $true 'replacing it with the read-only token (-SetupGitHubToken) fixes it'

	Write-SimStep '6. The token expires: git erases it, the message names the fix'
	Start-FakeGitHub $ro2     # the server now only accepts a different read token
	$failed = $false; $msg = ''
	try { Invoke-Git $cfg @('fetch', '--quiet', 'origin', 'main') | Out-Null } catch { $failed = $true; $msg = "$_" }
	Assert-Sim ($failed -and $msg -match '-SetupGitHubToken') 'the failed fetch says how to fix it (-SetupGitHubToken)'
	Assert-Sim (-not (Test-DailyGitHubTokenStored $cfg)) 'git told Credential Manager to forget the rejected token'
	$noToken = Invoke-GitHubAccessStep $cfg *>&1 | Out-String
	Assert-Sim ($noToken -match 'Geen token ingesteld' -and $noToken -match 'SetupGitHubToken') 'Prepare without a token and without a console warns with the exact command (and does not probe the PC''s own logins)'
	Set-DailyGitHubToken $cfg (ConvertTo-Secure $ro2)
	Invoke-Git $cfg @('fetch', '--quiet', 'origin', 'main') | Out-Null
	Assert-Sim (@(Get-StoredCredentialFiles).Count -eq 1) 'replacing the token leaves exactly one per-URL credential'

	Write-SimStep '7. The PC''s existing logins were never touched'
	$loginsAfter = Get-MachineLoginState
	$diff = @(Compare-Object ($loginsBefore -split '\|') ($loginsAfter -split '\|') | ForEach-Object { "$($_.SideIndicator) $($_.InputObject)" })
	Assert-Sim ($loginsAfter -ceq $loginsBefore) "the global git config and every other Credential Manager entry are byte-for-byte unchanged after set, clone, fetch, expiry, replace ($($diff -join '; '))"
	Assert-Sim ((Get-Content -Raw (Get-ChildItem -LiteralPath $env:GCM_PLAINTEXT_STORE_PATH -Recurse -File | Where-Object { $_.FullName -like '*yoobiuser*' } | Select-Object -First 1).FullName) -match 'YOOBI_LOGIN_MUST_STAY') 'the other login is still there with its password'

	Write-SimStep '8. No leaks'
	Stop-Transcript | Out-Null
	# gitconfig is the simulation's imitation of `gh auth setup-git`: it holds the
	# fake gh tokens on purpose.
	$leaks = @(Find-SecretLeaks $root @($ro, $ro2, $rw, $other) -SkipDirs @('gcm-store', 'srv', 'seed') -SkipFiles @('gitconfig', 'gh-helper.config'))
	Assert-Sim ($leaks.Count -eq 0) "no token in any log, state, config or transcript under the scratch folder ($($leaks -join '; '))"
	Assert-Sim ((Get-Content (Join-Path $root 'requests.log') -Raw) -notmatch 'github_pat_') 'the fake server log holds token kinds only, no values'
	$stored = (@(Get-StoredCredentialFiles) | ForEach-Object { Get-Content -LiteralPath $_.FullName -Raw }) -join ''
	Assert-Sim ($stored -match [regex]::Escape($ro2)) 'the only place the token lives is the (simulated) credential store'
	Start-Transcript -LiteralPath $transcript -Append | Out-Null
} finally {
	try { Stop-Transcript | Out-Null } catch { }
	if ($script:serverPid) { Stop-Process -Id $script:serverPid -Force -ErrorAction SilentlyContinue }
	if (-not $Keep) { Start-Sleep -Milliseconds 800; Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}
$fails = Write-SimSummary
exit $(if ($fails -eq 0) { 0 } else { 1 })
