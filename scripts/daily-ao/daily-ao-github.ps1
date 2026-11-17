# daily-ao-github.ps1
# GitHub access for the daily checkout that does not depend on which `gh` account
# is active.
#
# The daily checkout fetches one private repository. Git asks its credential
# helpers for a login, and with `gh auth setup-git` (or any other helper keyed on
# github.com) the answer is "whoever is active in gh right now". A `gh auth switch`
# to another account then breaks the fetch. Fix: a fine-grained, READ-ONLY token
# for exactly this repository, stored in Git Credential Manager (Windows
# Credential Manager) under this repository's URL only, and a per-URL credential
# setting in the daily checkout that ignores every other helper:
#
#   [credential "https://github.com/<owner>/<repo>.git"]
#       helper =            <- empty value: forget the helpers from other config
#       helper = manager    <- Git Credential Manager only
#       useHttpPath = true  <- the repo path is part of the stored identity
#       provider = generic  <- no GitHub OAuth sign-in flow, just the stored token
#       username = <owner>
#
# The token is only ever read from a hidden prompt, held in memory, handed to
# Git on stdin / in the environment of one child process, and never written to a
# file, a log or a command line.
#
# What this does NOT touch (the PC may hold other logins, for example for a
# company's own code host): no `git config --global/--system`, no `gh auth`
# command, no change to any credential helper or stored login. Everything is
# either a `-c` option on one git command or a setting in the daily checkout's own
# .git/config, scoped to this repository's URL, plus ONE new Credential Manager
# entry named after that URL. Git is never asked to probe with the machine's
# normal helpers: after a plain fetch git tells EVERY configured helper to store
# or erase the login it used, which could rewrite someone else's entry. Every call
# here either resets the helper list for this URL first, or only reads.
#Requires -Version 7.4

# ---------------------------------------------------------------------------
# The repository and the credential settings
# ---------------------------------------------------------------------------
# A token only makes sense for an http(s) URL of the form https://host/owner/repo.git
# (a local path, as a simulation may use, has no login).
$script:RepoUrlPattern = '^(?<scheme>https?)://(?<host>[A-Za-z0-9.-]+(:\d+)?)/(?<owner>[A-Za-z0-9._-]+)/(?<repo>[A-Za-z0-9._-]+?)(?:\.git)?/?$'
function Test-GitHubTokenSupported($cfg) { return [bool]($cfg.RepoUrl -match $script:RepoUrlPattern) }

function Get-GitRepoRef($cfg) {
	if ($cfg.RepoUrl -notmatch $script:RepoUrlPattern) {
		throw "De repository-URL '$($cfg.RepoUrl)' heeft niet de vorm https://host/eigenaar/repo.git; een token kan daar niet voor worden ingesteld."
	}
	$scheme = $Matches['scheme']; $hostName = $Matches['host']; $owner = $Matches['owner']; $repo = $Matches['repo']
	[pscustomobject]@{ Scheme = $scheme; Host = $hostName; Owner = $owner; Repo = $repo; Url = "${scheme}://$hostName/$owner/$repo.git"; Path = "$owner/$repo.git" }
}

# The settings above as key/value objects. Used as `-c key=value` on a command
# line, and written into the daily checkout's own config.
function Get-DailyCredentialSettings($cfg) {
	$s = "credential.$((Get-GitRepoRef $cfg).Url)"
	[pscustomobject]@{ Key = "$s.helper"; Value = '' }
	[pscustomobject]@{ Key = "$s.helper"; Value = 'manager' }
	[pscustomobject]@{ Key = "$s.useHttpPath"; Value = 'true' }
	[pscustomobject]@{ Key = "$s.provider"; Value = 'generic' }
	[pscustomobject]@{ Key = "$s.username"; Value = (Get-GitRepoRef $cfg).Owner }
}

function ConvertTo-GitConfigArgs($Settings) { foreach ($x in $Settings) { '-c'; "$($x.Key)=$($x.Value)" } }

# Same shape, but the helper answers with the token in $AO_DAILY_VERIFY_TOKEN. Used
# to check a new token BEFORE it is stored, so a wrong token is never kept.
function Get-VerifyHelperArgs($cfg) {
	$ref = Get-GitRepoRef $cfg
	$s = "credential.$($ref.Url)"
	'-c'; "$s.helper="
	'-c'; "$s.helper=!f() { echo username=$($ref.Owner); echo password=`$AO_DAILY_VERIFY_TOKEN; }; f"
	'-c'; "$s.useHttpPath=true"
}

# Git credential helper requests are made from a throwaway cwd so the daily
# checkout's own config (or whatever repo the caller is in) cannot change the result.
function Invoke-GitText {
	param($cfg, [string[]]$GitArgs, [string]$Stdin, [hashtable]$Env, [string]$WorkingDirectory = ([IO.Path]::GetTempPath()))
	# Never wait for a terminal prompt or open a Git Credential Manager window: these
	# calls run from hidden launchers. Set for this one call only; agents the daemon
	# starts later must not inherit them.
	$vars = @{ GIT_TERMINAL_PROMPT = '0'; GCM_INTERACTIVE = 'never' }
	if ($Env) { foreach ($k in $Env.Keys) { $vars[$k] = [string]$Env[$k] } }
	$saved = @{}
	foreach ($k in $vars.Keys) { $saved[$k] = [Environment]::GetEnvironmentVariable($k); [Environment]::SetEnvironmentVariable($k, $vars[$k]) }
	$prev = Get-Location
	try {
		Set-Location -LiteralPath $WorkingDirectory
		$out = if ($PSBoundParameters.ContainsKey('Stdin')) { $Stdin | & git @GitArgs 2>&1 } else { & git @GitArgs 2>&1 }
		$code = $LASTEXITCODE
	} finally {
		Set-Location -LiteralPath $prev
		foreach ($k in $vars.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k]) }
	}
	[pscustomobject]@{ ExitCode = $code; Output = (@($out | ForEach-Object { "$_" }) -join "`n") }
}

# 'auth' (credentials missing, wrong, expired or without access), 'network' or 'other'.
function Get-GitFailureKind([string]$Text) {
	if ($Text -match '(?i)authentication failed|could not read (username|password)|terminal prompts disabled|interactivity has been disabled|invalid (credentials|username or password)|returned error: 40[134]|permission to .* denied|repository (not found|.{0,300} not found)|HTTP/\S+ 40[134]') { return 'auth' }
	if ($Text -match '(?i)could not resolve host|timed out|connection (refused|reset|was reset)|network is unreachable|failed to connect|unable to access|could not connect|ssl|certificate') { return 'network' }
	return 'other'
}

# Hint added to a failed fetch/clone when the cause is the login.
function Get-SetupTokenCommand { "pwsh -File `"$(Join-Path $PSScriptRoot 'start-daily-ao.ps1')`" -SetupGitHubToken" }

function Get-GitAuthHint { "GitHub weigert de toegang (het alleen-lezen token ontbreekt, is verlopen of ingetrokken, of het actieve GitHub-account heeft geen toegang). Herstel: $(Get-SetupTokenCommand)" }

# ---------------------------------------------------------------------------
# Checks (read-only)
# ---------------------------------------------------------------------------
# Is a token stored for exactly this repository URL? Asks Git Credential Manager
# the way Git would; the answer is discarded at once, only yes/no is kept.
function Test-DailyGitHubTokenStored($cfg) {
	if (-not (Test-GitHubTokenSupported $cfg)) { return $false }
	$ref = Get-GitRepoRef $cfg
	$gitArgs = @(ConvertTo-GitConfigArgs (Get-DailyCredentialSettings $cfg)) + @('credential', 'fill')
	$r = Invoke-GitText $cfg $gitArgs -Stdin "protocol=$($ref.Scheme)`nhost=$($ref.Host)`npath=$($ref.Path)`nusername=$($ref.Owner)`n`n"
	$has = ($r.ExitCode -eq 0 -and $r.Output -match '(?m)^password=.+')
	$r = $null
	return [bool]$has
}

# Can the branch be read? Mode 'stored' = with the per-URL token only; 'custom' =
# with the helper arguments given (a token being checked before it is stored).
# There is deliberately no mode that uses the machine's own helpers: see the top.
function Test-DailyGitHubAccess($cfg, [ValidateSet('stored', 'custom')][string]$Mode, [string[]]$AuthArgs = @(), [hashtable]$Env) {
	$ref = Get-GitRepoRef $cfg
	if ($Mode -eq 'custom' -and $AuthArgs.Count -eq 0) { throw 'Mode custom needs the helper arguments.' }
	$pre = if ($Mode -eq 'custom') { $AuthArgs } else { @(ConvertTo-GitConfigArgs (Get-DailyCredentialSettings $cfg)) }
	$r = Invoke-GitText $cfg (@($pre) + @('ls-remote', '--heads', $ref.Url, "refs/heads/$($cfg.Branch)")) -Env $Env
	$ok = ($r.ExitCode -eq 0 -and $r.Output -match '(?m)^[0-9a-f]{40}\s')
	[pscustomobject]@{ Ok = $ok; Kind = $(if ($ok) { 'ok' } else { Get-GitFailureKind $r.Output }); Detail = $r.Output }
}

# Maps a dry-run push to what the token may do. Separate so it can be tested.
function Get-WriteProbeVerdict([int]$ExitCode, [string]$Text) {
	if ($ExitCode -eq 0) { return 'write-capable' }
	if ($Text -match '(?i)returned error: 401|HTTP/\S+ 401|authentication failed|invalid (credentials|username or password)') { return 'invalid' }
	if ($Text -match '(?i)returned error: 403|HTTP/\S+ 403|permission to .* denied') { return 'read-only' }
	return 'unknown'
}

# A dry-run push needs write access just to be told the refs; nothing is sent or
# created. 'read-only' (denied), 'write-capable' (it would have worked),
# 'invalid' (login refused) or 'unknown'.
function Test-DailyGitHubWriteAccess($cfg, [string[]]$AuthArgs, [hashtable]$Env) {
	$ref = Get-GitRepoRef $cfg
	$tmp = Join-Path ([IO.Path]::GetTempPath()) ('ao-daily-probe-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
	try {
		New-Item -ItemType Directory -Force -Path $tmp | Out-Null
		[void](Invoke-GitText $cfg @('init', '-q', $tmp))
		[void](Invoke-GitText $cfg @('-c', 'user.name=ao-daily', '-c', 'user.email=ao-daily@example.invalid', 'commit', '-q', '--allow-empty', '-m', 'probe') -WorkingDirectory $tmp)
		$r = Invoke-GitText $cfg (@($AuthArgs) + @('push', '--dry-run', '--porcelain', $ref.Url, 'HEAD:refs/heads/ao-daily-token-probe')) -Env $Env -WorkingDirectory $tmp
		return Get-WriteProbeVerdict $r.ExitCode $r.Output
	} finally { Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue }
}

# Read-only summary for the switch check: changes nothing.
function Write-GitHubAccessSummary($cfg) {
	try {
		if (-not (Add-GitToPathIfInstalled)) { Write-Warn2 'Git ontbreekt; zie start-daily-ao.ps1 -Prepare'; return }
		if (-not (Test-GitHubTokenSupported $cfg)) { Write-Note "GitHub-token: niet van toepassing op $($cfg.RepoUrl)"; return }
		$ref = Get-GitRepoRef $cfg
		if (Test-DailyGitHubTokenStored $cfg) {
			$a = Test-DailyGitHubAccess $cfg 'stored'
			if ($a.Ok) { Write-Ok "GitHub: alleen-lezen token voor $($ref.Owner)/$($ref.Repo) werkt (los van je actieve gh-account)" }
			else { Write-Warn2 "GitHub: er staat een token voor $($ref.Owner)/$($ref.Repo), maar het werkt niet ($($a.Kind)); herstel met start-daily-ao.ps1 -SetupGitHubToken" }
		} else {
			# Not probed: asking git with the machine's own helpers could make them store
			# or erase a login that belongs to something else.
			Write-Note "GitHub: geen eigen token voor $($ref.Owner)/$($ref.Repo) ingesteld. Ophalen gebruikt dan de gewone git/gh-aanmelding van deze pc (en volgt het actieve gh-account). Aanbevolen: start-daily-ao.ps1 -SetupGitHubToken"
		}
	} catch { Write-Warn2 "GitHub-toegang controleren mislukt: $_" }
}

# ---------------------------------------------------------------------------
# Storing the token
# ---------------------------------------------------------------------------
function Test-InteractiveSession { [Environment]::UserInteractive -and -not [Console]::IsInputRedirected -and -not [Console]::IsOutputRedirected }

# A simulation must never write into the real Windows Credential Manager: Git
# Credential Manager has a plain-file store for exactly this, kept inside the
# simulation's own AO home.
function Assert-SafeCredentialStore($cfg) {
	if (-not $cfg.IsSimulation) { return }
	$store = $env:GCM_PLAINTEXT_STORE_PATH
	if ($env:GCM_CREDENTIAL_STORE -ne 'plaintext' -or -not $store -or -not (Test-PathInside $store $cfg.AoHome)) {
		throw 'A simulation must run with GCM_CREDENTIAL_STORE=plaintext and GCM_PLAINTEXT_STORE_PATH inside its own AO home (never the real Windows Credential Manager).'
	}
}

function Show-GitHubTokenInstructions($cfg) {
	$ref = Get-GitRepoRef $cfg
	Write-Host ''
	Write-Host "Een eigen alleen-lezen token voor $($ref.Owner)/$($ref.Repo) maken (eenmalig, ongeveer 2 minuten):" -ForegroundColor Cyan
	Write-Host "  1. Log in op github.com als **$($ref.Owner)** (niet met je werkaccount; gebruik zo nodig een privevenster)."
	Write-Host '  2. Open https://github.com/settings/personal-access-tokens/new'
	Write-Host '     (of: je profielfoto > Settings > Developer settings > Personal access tokens > Fine-grained tokens > Generate new token)'
	Write-Host "  3. Token name: bijvoorbeeld 'ao-daily-pc2'. Expiration: kies zelf (maximaal 1 jaar; daarna maak je een nieuw token)."
	Write-Host "  4. Resource owner: $($ref.Owner). Repository access: 'Only select repositories' en kies alleen $($ref.Repo)."
	Write-Host "  5. Repository permissions: zet 'Contents' op 'Read-only'. Dat is het enige recht dat je nodig hebt;"
	Write-Host "     'Metadata: Read-only' staat er door GitHub automatisch bij. Zet verder niets aan."
	Write-Host "  6. Klik 'Generate token' en kopieer het token (begint met github_pat_). GitHub toont het maar een keer."
	Write-Host ''
	Write-Host 'Plak het token alleen hieronder, in dit venster. Niet in een chat, e-mail of bestand.' -ForegroundColor Yellow
	Write-Host 'Het wordt opgeslagen in Windows-referentiebeheer, alleen voor deze repository-URL.'
	Write-Host ''
}

# Takes a token off the credential store again (after a failed check).
function Remove-DailyGitHubToken($cfg) {
	$ref = Get-GitRepoRef $cfg
	$gitArgs = @(ConvertTo-GitConfigArgs (Get-DailyCredentialSettings $cfg)) + @('credential', 'reject')
	[void](Invoke-GitText $cfg $gitArgs -Stdin "protocol=$($ref.Scheme)`nhost=$($ref.Host)`npath=$($ref.Path)`nusername=$($ref.Owner)`n`n")
}

# Writes the per-URL credential settings into the daily checkout (so plain `git`
# there uses the token too) and pins origin to the exact URL they apply to. Only
# when a token is stored: without one nothing changes. Returns 'configured',
# 'none' or 'no-checkout'.
function Initialize-DailyGitAuth($cfg) {
	if (-not (Test-Path -LiteralPath (Join-Path $cfg.Checkout '.git'))) { return 'no-checkout' }
	if (-not (Test-DailyGitHubTokenStored $cfg)) { return 'none' }
	$ref = Get-GitRepoRef $cfg
	[void](& git -C $cfg.Checkout config --local --remove-section "credential.$($ref.Url)" 2>&1)
	foreach ($x in Get-DailyCredentialSettings $cfg) {
		& git -C $cfg.Checkout config --local --add $x.Key $x.Value
		if ($LASTEXITCODE -ne 0) { throw "git config $($x.Key) failed" }
	}
	$origin = ([string](& git -C $cfg.Checkout remote get-url origin 2>$null)).Trim()
	if ($origin -ne $ref.Url) {
		& git -C $cfg.Checkout remote set-url origin $ref.Url
		Write-Note "origin staat nu op $($ref.Url) (de URL waar het token bij hoort)"
	}
	return 'configured'
}

# Asks for the token (or takes a SecureString from a test), checks it, and only
# then stores it. Throws without storing anything when the token is wrong, can
# write, or cannot read this repository.
function Set-DailyGitHubToken($cfg, [securestring]$Token) {
	Assert-GitAvailable
	if (-not (Test-GitCredentialManager)) {
		throw 'Git Credential Manager ontbreekt. Installeer Git for Windows opnieuw en laat "Git Credential Manager" aangevinkt, of installeer die van https://github.com/git-ecosystem/git-credential-manager.'
	}
	Assert-SafeCredentialStore $cfg
	$ref = Get-GitRepoRef $cfg
	if (-not $Token) {
		Show-GitHubTokenInstructions $cfg
		$Token = Read-Host -AsSecureString 'Plak het token hier (je ziet niets verschijnen) en druk op Enter'
		if ($Token.Length -eq 0) { throw [ArgumentException]::new('Geen token ingevuld; er is niets opgeslagen.') }
	}
	$plain = [Net.NetworkCredential]::new('', $Token).Password
	try {
		if ($plain -notmatch '^github_pat_[A-Za-z0-9_]{20,}$') {
			throw [ArgumentException]::new('Dit lijkt geen fine-grained token (die begint met github_pat_). Een klassiek token geeft veel meer toegang dan nodig en wordt niet gebruikt. Maak een fine-grained token met alleen Contents: Read-only voor deze ene repository.')
		}
		# 1. Check it, without storing anything.
		$auth = @(Get-VerifyHelperArgs $cfg)
		$env1 = @{ AO_DAILY_VERIFY_TOKEN = $plain }
		$read = Test-DailyGitHubAccess $cfg 'custom' -AuthArgs $auth -Env $env1
		if (-not $read.Ok) {
			if ($read.Kind -eq 'network') { throw 'GitHub is niet bereikbaar (netwerk of proxy?). Het token is niet gecontroleerd en niet opgeslagen; probeer het opnieuw als je online bent.' }
			throw [ArgumentException]::new("Dit token geeft geen toegang tot $($ref.Owner)/$($ref.Repo). Controleer: Resource owner is $($ref.Owner), 'Only select repositories' bevat $($ref.Repo), en Contents staat op Read-only. Het token is niet opgeslagen.")
		}
		$verdict = Test-DailyGitHubWriteAccess $cfg -AuthArgs $auth -Env $env1
		if ($verdict -eq 'write-capable') { throw [ArgumentException]::new("Dit token mag ook SCHRIJVEN naar $($ref.Owner)/$($ref.Repo). Dat is meer dan nodig. Maak een nieuw token met alleen Contents: Read-only. Het token is niet opgeslagen.") }
		if ($verdict -eq 'invalid') { throw [ArgumentException]::new('GitHub weigert dit token (ongeldig of verlopen). Het token is niet opgeslagen.') }
		if ($verdict -eq 'unknown') { Write-Warn2 'kon niet vaststellen of het token alleen-lezen is; controleer in GitHub dat alleen Contents: Read-only aanstaat.' }
		# 2. Store it for exactly this URL.
		$approve = @(ConvertTo-GitConfigArgs (Get-DailyCredentialSettings $cfg)) + @('credential', 'approve')
		$r = Invoke-GitText $cfg $approve -Stdin "protocol=$($ref.Scheme)`nhost=$($ref.Host)`npath=$($ref.Path)`nusername=$($ref.Owner)`npassword=$plain`n`n"
		if ($r.ExitCode -ne 0) { throw "Opslaan in Git Credential Manager mislukte (exit $($r.ExitCode)): $($r.Output)" }
	} finally { $plain = $null; $env1 = $null }
	# 3. Prove the stored copy works on its own.
	$again = Test-DailyGitHubAccess $cfg 'stored'
	if (-not $again.Ok) {
		Remove-DailyGitHubToken $cfg
		throw "Het token is opgeslagen maar werkt niet bij het teruglezen ($($again.Kind)). Het is weer verwijderd. Controleer Git Credential Manager (git credential-manager --version)."
	}
	[void](Initialize-DailyGitAuth $cfg)
	Write-Ok "alleen-lezen token voor $($ref.Owner)/$($ref.Repo) opgeslagen in Windows-referentiebeheer (alleen voor $($ref.Url)) en gecontroleerd"
	Write-Note "Het token staat in Windows-referentiebeheer (Windows-referenties) onder `"git:$($ref.Url)`". Verwijderen kan daar, en intrekken op github.com bij Settings > Developer settings."
}

# The guided step of -Prepare (and the whole of -SetupGitHubToken with -Force).
function Invoke-GitHubAccessStep($cfg, [switch]$Force) {
	Assert-GitAvailable
	$ref = Get-GitRepoRef $cfg
	Write-Step "GitHub-toegang voor $($ref.Owner)/$($ref.Repo)"
	$interactive = Test-InteractiveSession
	if (-not $Force) {
		if (Test-DailyGitHubTokenStored $cfg) {
			$a = Test-DailyGitHubAccess $cfg 'stored'
			if ($a.Ok) {
				# A token stored by hand (the guide's clone step) was not checked for
				# write access yet: do it now, and refuse one that can write.
				$verdict = Test-DailyGitHubWriteAccess $cfg -AuthArgs @(ConvertTo-GitConfigArgs (Get-DailyCredentialSettings $cfg))
				if ($verdict -eq 'write-capable') {
					throw "Het opgeslagen token mag ook SCHRIJVEN naar $($ref.Owner)/$($ref.Repo). Dat is meer dan nodig. Maak een nieuw token met alleen Contents: Read-only (stap 1 van de handleiding) en run start-daily-ao.ps1 -SetupGitHubToken; dat vervangt het opgeslagen token."
				}
				if ($verdict -eq 'unknown') { Write-Warn2 'kon niet vaststellen of het opgeslagen token alleen-lezen is; controleer in GitHub dat alleen Contents: Read-only aanstaat.' }
				[void](Initialize-DailyGitAuth $cfg)
				Write-Ok 'alleen-lezen token werkt (Windows-referentiebeheer, alleen voor deze repository-URL, los van je actieve gh-account)'
				return
			}
			if ($a.Kind -eq 'network') { Write-Warn2 'GitHub is niet bereikbaar; het opgeslagen token is niet gecontroleerd.'; return }
			Write-Warn2 'er staat een token, maar GitHub accepteert het niet (verlopen of ingetrokken). Een nieuw token is nodig.'
		} else {
			# No token of our own yet. Whether the machine's normal login happens to
			# reach this repository is deliberately not tested (see the top of this file).
			Write-Note "Er is nog geen eigen token voor $($ref.Owner)/$($ref.Repo). Zonder token gebruikt Git de gewone aanmelding van deze pc (gh of Git Credential Manager) en volgt het ophalen het actieve GitHub-account."
			Write-Note 'Het token wordt alleen voor deze ene repository-URL opgeslagen; bestaande git- en gh-aanmeldingen van deze pc (ook voor andere code) blijven onaangeraakt.'
			if (-not $interactive) {
				Write-Warn2 "Geen token ingesteld en dit venster kan niets vragen. Instellen: $(Get-SetupTokenCommand)"
				return
			}
			if ((Read-Host 'Nu een alleen-lezen token instellen? (j/n)') -notmatch '^(j|ja|y|yes)$') {
				Write-Warn2 "Overgeslagen: het ophalen hangt af van het actieve gh-account. Later instellen: $(Get-SetupTokenCommand)"
				return
			}
		}
	}
	if (-not $interactive) {
		throw ('Er is een GitHub-token nodig, maar dit venster kan niets vragen. Draai zelf, in een eigen PowerShell-venster: ' +
			"$(Get-SetupTokenCommand)  (plak het token nooit in een chat met een agent).")
	}
	# A wrong token is the likeliest mistake: let the user try again (3 times). Other
	# errors (no Git Credential Manager, no network) are not about the token and stop.
	for ($try = 1; $try -le 3; $try++) {
		try { Set-DailyGitHubToken $cfg; return }
		catch [ArgumentException] {
			if ($try -eq 3) { throw }
			Write-Warn2 "$($_.Exception.Message)"
			Write-Host "Probeer het opnieuw (poging $($try + 1) van 3)." -ForegroundColor Yellow
		}
	}
}
