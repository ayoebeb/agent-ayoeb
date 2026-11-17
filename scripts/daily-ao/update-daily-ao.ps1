<#
.SYNOPSIS
  Brings the daily AO up to origin/main.

.DESCRIPTION
  Looks at every merged commit (first-parent) between the checkout and
  origin/main and sorts it:
    renderer  UI-only (React/CSS): applied at once, Vite hot reload shows it.
    none      docs/tests/CI: applied at once, nothing to reload.
    restart   Electron main/preload/shared/config/build scripts: Electron restart.
    daemon    backend (Go): rebuild daemon + restart.
    deps      package-lock: npm ci + restart.
  Commits are applied in order. The leading run of renderer/none commits is
  applied immediately; from the first commit that needs a restart on, nothing
  is applied until you say yes (so nothing reloads half-way).

  A restart keeps every session running: the daemon stops gracefully and the
  new one adopts the running pty-hosts. Sessions that do not come back are
  restored automatically.

  Every backend build also refreshes the stable `ao` (<daily>\bin\ao.exe, on the
  user PATH) so new terminals and sessions run the same commit as the daemon.

  -Yes             Apply restarts without asking.
  -NonInteractive  Never ask. Exit code 10 = a restart is waiting.
  -Watch           Keep checking every -IntervalSeconds (default 30). Restarts
                   are never applied in watch mode; they are announced.
#>
[CmdletBinding()]
param(
	[switch]$Yes,
	[switch]$NonInteractive,
	[switch]$Watch,
	[int]$IntervalSeconds = 30,
	[string]$ConfigFile,
	[int]$ReadyTimeoutSeconds = 300
)
. (Join-Path $PSScriptRoot 'daily-ao-common.ps1')

$cfg = Get-DailyConfig -ConfigFile $ConfigFile
Initialize-DailyDirs $cfg
$pendingFile = Join-Path $cfg.DailyDir 'pending-restart.json'

function Get-ChangeKind([string]$Path) {
	$p = $Path -replace '\\', '/'
	switch -Regex ($p) {
		'^backend/' { return 'daemon' }
		'^frontend/package(-lock)?\.json$' { return 'deps' }
		'^packages/[^/]+/package(-lock)?\.json$' { return 'deps' }
		'\.(test|spec)\.(ts|tsx|mts|mjs|js)$' { return 'none' }
		'\.d\.ts$' { return 'none' }
		'^frontend/src/renderer/' { return 'renderer' }
		'^frontend/src/styles/' { return 'renderer' }
		'^frontend/src/docs/' { return 'renderer' }
		'^frontend/src/api/' { return 'renderer' }
		'^frontend/index\.html$' { return 'renderer' }
		'^frontend/public/' { return 'renderer' }
		'^packages/product-ui/src/' { return 'renderer' }
		'^frontend/(e2e|test|docs)/' { return 'none' }
		'^(docs|test|\.github|\.agents|skills)/' { return 'none' }
		'\.md$' { return 'none' }
		default { return 'restart' }
	}
}

$rank = @{ none = 0; renderer = 1; restart = 2; deps = 3; daemon = 3 }

function Get-UpdatePlan {
	Invoke-Git $cfg @('fetch', '--quiet', 'origin', $cfg.Branch) | Out-Null
	Assert-ReviewedCheckout $cfg
	$head = (Invoke-Git $cfg @('rev-parse', 'HEAD')).Trim()
	$target = (Invoke-Git $cfg @('rev-parse', "origin/$($cfg.Branch)")).Trim()
	$commits = @()
	if ($head -ne $target) {
		$list = Invoke-Git $cfg @('rev-list', '--first-parent', '--reverse', "$head..$target")
		foreach ($c in @($list)) {
			$c = ([string]$c).Trim()
			if (-not $c) { continue }
			$files = @(Invoke-Git $cfg @('diff', '--name-only', "$c^1", $c) | ForEach-Object { ([string]$_).Trim() } | Where-Object { $_ })
			$kinds = @($files | ForEach-Object { Get-ChangeKind $_ } | Sort-Object -Unique)
			$subject = (Invoke-Git $cfg @('log', '-1', '--format=%s', $c)).Trim()
			$commits += [pscustomobject]@{ Sha = $c; Subject = $subject; Kinds = $kinds; Files = $files
				NeedsRestart = [bool]($kinds | Where-Object { $rank[$_] -ge 2 }) }
		}
	}
	$auto = $null; $rest = @(); $blocked = $false
	foreach ($c in $commits) {
		if (-not $blocked -and -not $c.NeedsRestart) { $auto = $c; continue }
		$blocked = $true; $rest += $c
	}
	$allKinds = @($rest | ForEach-Object { $_.Kinds } | Sort-Object -Unique)
	[pscustomobject]@{ Head = $head; Target = $target; Commits = $commits; Auto = $auto; Rest = $rest; RestKinds = $allKinds }
}

function Show-RestartSummary($plan) {
	$what = @()
	if ($plan.RestKinds -contains 'daemon') { $what += 'backend opnieuw bouwen' }
	if ($plan.RestKinds -contains 'deps') { $what += 'npm ci' }
	$what += 'AO herstarten (sessies blijven lopen)'
	Write-Step "Update klaar die een herstart nodig heeft: $($what -join ', ')"
	foreach ($c in $plan.Rest) { Write-Note ("{0}  [{1}]  {2}" -f $c.Sha.Substring(0, 9), ($c.Kinds -join ','), $c.Subject) }
}

function Invoke-RestartUpdate($plan) {
	$goRoot = Install-GoToolchain $cfg
	$nodeDir = Install-NodeToolchain $cfg
	Set-DailyEnv $cfg $goRoot $nodeDir
	$state = Read-DailyState $cfg
	$old = $plan.Head
	$daemon = Get-CurrentDaemon $cfg
	$before = @()
	if ($daemon -and $daemon.Kind -eq 'daily') { $before = Save-SessionSnapshot $cfg $daemon.Port 'before-update' $state }
	$wasRunning = @((Get-DailyProcesses $cfg).ElectronMain).Count -gt 0
	$needsDeps = $plan.RestKinds -contains 'deps'

	# npm ci rewrites node_modules, which the running Electron holds open: stop
	# first. Otherwise build while the old version still serves (less downtime).
	if ($needsDeps -and $wasRunning) { Write-Step 'AO daily stoppen voor npm ci'; Stop-DailyFrontend $cfg }
	Invoke-Git $cfg @('merge', '--ff-only', '--quiet', $plan.Target) | Out-Null
	try {
		Install-FrontendDeps $cfg $state
		Update-Builds $cfg $state
	} catch {
		Write-Warn2 "Bouwen mislukt; checkout terug naar $($old.Substring(0, 9)). $_"
		Invoke-Git $cfg @('reset', '--keep', '--quiet', $old) | Out-Null
		if ($needsDeps) { Install-FrontendDeps $cfg $state -Force }
		Write-DailyState $cfg $state
		if ($needsDeps -and $wasRunning) {
			$l = Start-DailyFrontendForUser $cfg $goRoot $nodeDir
			if ($l) { [void](Wait-DaemonReady $cfg $l.StartedUtc 'daily' $ReadyTimeoutSeconds) }
		}
		throw
	}
	Write-DailyState $cfg $state
	if (-not $wasRunning) {
		$note = if ($daemon -and $daemon.Kind -eq 'daily') { 'De daemon zonder venster (sinds het opstarten van de pc) draait de vorige versie tot je je aanmeldt; dan start de nieuwe.' } else { 'Start hem met de snelkoppeling.' }
		Write-Ok "Bijgewerkt naar $($plan.Target.Substring(0, 9)); het AO-venster draaide niet. $note"
		if (Test-Path -LiteralPath $pendingFile) { Remove-Item -LiteralPath $pendingFile -Force }
		return
	}
	Write-Step 'AO daily herstarten'
	Stop-DailyFrontend $cfg
	Sync-DesktopCredentials $cfg
	# From session 0 (an agent started before sign-in) the window is opened on
	# the user's desktop through the "Ayo (daily) openen" task.
	$launch = Start-DailyFrontendForUser $cfg $goRoot $nodeDir
	if (-not $launch) { throw 'Niemand is aangemeld; het venster kan niet openen. Ayo (daily) start bij het aanmelden met de nieuwe versie.' }
	$ready = Wait-DaemonReady $cfg $launch.StartedUtc 'daily' $ReadyTimeoutSeconds
	Write-Ok "daemon PID $($ready.Pid) ($($ready.Exe))"
	$restored = Confirm-SessionsSurvived $cfg $ready.Port $before (Get-DaemonManifestPath $cfg)
	$state['forgePid'] = $launch.ForgePid
	$state['startedCommit'] = $plan.Target
	$state['lastUpdate'] = @{ at = (Get-Date).ToString('o'); from = $old; to = $plan.Target; kinds = $plan.RestKinds; sessionsBefore = @($before).Count; restored = @($restored) }
	$state['pendingSessions'] = @()
	Write-DailyState $cfg $state
	if (Test-Path -LiteralPath $pendingFile) { Remove-Item -LiteralPath $pendingFile -Force }
	Write-Ok "AO daily draait op $($plan.Target.Substring(0, 12))."
}

# A pending-restart.json that no longer matches a waiting update must not keep
# the app's notice up.
function Remove-PendingFile {
	if (Test-Path -LiteralPath $pendingFile) { Remove-Item -LiteralPath $pendingFile -Force -ErrorAction SilentlyContinue }
}

# True while the Ayo window shows its own "update ready" notice for this target
# (the app refreshes update-notice-shown.json every few seconds while it does).
function Test-AppShowsNotice([string]$Target) {
	$file = Join-Path $cfg.DailyDir 'update-notice-shown.json'
	if (-not (Test-Path -LiteralPath $file)) { return $false }
	try {
		$n = Get-Content -Raw -LiteralPath $file | ConvertFrom-Json
		return ($n.target -eq $Target) -and (((Get-Date) - [datetime]$n.at).TotalSeconds -lt 60)
	} catch { return $false }
}

function Show-Balloon([string]$Text) {
	try {
		Add-Type -AssemblyName System.Windows.Forms
		$n = New-Object System.Windows.Forms.NotifyIcon
		$n.Icon = [System.Drawing.SystemIcons]::Information
		$n.Visible = $true
		$n.ShowBalloonTip(15000, 'AO daily', $Text, [System.Windows.Forms.ToolTipIcon]::Info)
		Start-Sleep -Seconds 16
		$n.Dispose()
	} catch { }
}

# One pass, then the installed scripts follow the checkout (scripts/daily-ao on
# main). Returns 'current', 'applied' or 'restart-pending'.
function Invoke-UpdateOnce([switch]$NeverRestart) {
	$result = Invoke-UpdatePass -NeverRestart:$NeverRestart
	Sync-DailyScriptsFromCheckout $cfg
	return $result
}

function Invoke-UpdatePass([switch]$NeverRestart) {
	$plan = Get-UpdatePlan
	if (@($plan.Commits).Count -eq 0) { Remove-PendingFile; return 'current' }
	if ($plan.Auto) {
		Invoke-Git $cfg @('merge', '--ff-only', '--quiet', $plan.Auto.Sha) | Out-Null
		$n = [array]::IndexOf(@($plan.Commits.Sha), $plan.Auto.Sha) + 1
		Write-Ok ("{0} UI-commit(s) toegepast tot {1}: zichtbaar via hot reload" -f $n, $plan.Auto.Sha.Substring(0, 9))
	}
	if (@($plan.Rest).Count -eq 0) { Remove-PendingFile; return 'applied' }
	Show-RestartSummary $plan
	# scriptPath/pwsh/configFile let the app run this same update with -Yes when the
	# user clicks Restart in its notice (also on a PC whose daily home differs).
	@{ at = (Get-Date).ToString('o'); target = $plan.Target; kinds = $plan.RestKinds; commits = @($plan.Rest | ForEach-Object { "$($_.Sha.Substring(0, 9)) $($_.Subject)" })
		scriptPath = (Join-Path $cfg.ScriptsDir 'update-daily-ao.ps1'); pwsh = (Get-PwshPath $cfg); configFile = $cfg.ConfigFile } |
		ConvertTo-Json | Set-Content -LiteralPath $pendingFile -Encoding utf8
	if ($NeverRestart -or $NonInteractive) { return 'restart-pending' }
	if (-not $Yes) {
		$answer = Read-Host 'Nu toepassen en AO herstarten? Lopende sessies blijven doorgaan. (j/n)'
		if ($answer -notmatch '^(j|ja|y|yes)$') { Write-Note 'Niet toegepast. Later: update-daily-ao.ps1'; return 'restart-pending' }
	}
	$plan = Get-UpdatePlan  # re-read: main may have moved while asking
	Invoke-RestartUpdate $plan
	return 'applied'
}

# One update at a time per AO home: the watcher and a manual run must not
# fast-forward the checkout at the same moment, also when the manual run comes
# from an agent in another Windows session (started before sign-in).
$updateLock = New-DailyMutex $cfg 'AoDailyUpdate'
function Enter-UpdateLock([int]$TimeoutMs) { Enter-DailyMutex $updateLock $TimeoutMs }

$transcript = Join-Path $cfg.LogDir ("update-{0}.log" -f (Get-Date -Format 'yyyyMMdd'))
Start-Transcript -LiteralPath $transcript -Append | Out-Null
try {
	if ($Watch) {
		Write-Step "Watcher: elke $IntervalSeconds s origin/$($cfg.Branch) controleren"
		$announced = ''
		$firstSeen = $null
		while ($true) {
			try {
				if (-not (Enter-UpdateLock 0)) { Start-Sleep -Seconds $IntervalSeconds; continue }
				try { $r = Invoke-UpdateOnce -NeverRestart } finally { $updateLock.ReleaseMutex() }
				if ($r -eq 'restart-pending') {
					$p = Get-Content -Raw -LiteralPath $pendingFile | ConvertFrom-Json
					if ($announced -ne $p.target) {
						if ($firstSeen -eq $null) { $firstSeen = Get-Date }
						$appRunning = @((Get-DailyProcesses $cfg).ElectronMain).Count -gt 0
						if (Test-AppShowsNotice $p.target) {
							$announced = $p.target; $firstSeen = $null
							Write-Note 'Update-melding staat in het Ayo-venster; geen Windows-melding.'
						} elseif ($appRunning -and ((Get-Date) - $firstSeen).TotalSeconds -lt 120) {
							# The window is up and will show its notice within a poll or two.
						} else {
						$announced = $p.target; $firstSeen = $null
						if (-not $cfg.IsSimulation) { Show-Balloon 'Er staat een AO-update klaar die een herstart nodig heeft. Sessies blijven lopen. Vraag de orchestrator of run update-daily-ao.ps1.' }
						}
					}
				}
			} catch { Write-Warn2 "update-controle mislukt: $_" }
			Start-Sleep -Seconds $IntervalSeconds
		}
	}
	if (-not (Enter-UpdateLock 600000)) { throw 'Een andere update loopt nog; probeer het zo opnieuw.' }
	try { $result = Invoke-UpdateOnce } finally { $updateLock.ReleaseMutex() }
	switch ($result) {
		'current' { Write-Ok "Al bij met origin/$($cfg.Branch)." }
		'restart-pending' { exit $script:ExitRestartPending }
	}
} finally {
	Stop-Transcript | Out-Null
}
