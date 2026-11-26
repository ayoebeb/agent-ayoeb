<#
.SYNOPSIS
  Starts the daily AO: this repository's origin/main as the primary AO, on the
  real data (same data dir, run file, port and Electron profile as the
  installed app).

.DESCRIPTION
  -Prepare              Only set up: checks Git, GitHub access (guides you to a
                        read-only token for this one repository when needed),
                        Go and Node under <AO home>\tools (installed there,
                        checksum-verified, when missing; never system-wide),
                        own checkout of main, npm deps, builds. Touches no
                        running AO.
  -SetupGitHubToken     Only the GitHub token: explains how to create a
                        fine-grained, read-only token (Contents: Read-only, this
                        one repository), checks it and stores it in Windows
                        Credential Manager for exactly this repository URL, so
                        fetching no longer depends on the active `gh` account.
                        Also how to replace an expired token. Run it yourself in
                        a PowerShell window; never paste the token into a chat.
  -SwitchFromInstalled  One-time switch: stops the installed AO without
                        stopping agents, moves its shortcuts and login items
                        aside, adds the Ayo (daily) login autostart, starts the
                        daily AO and checks every session survived. Runs the
                        switch check first and changes nothing if it fails.
  -CheckOnly            With -SwitchFromInstalled: only the switch check. Runs
                        every detection step against this machine and prints
                        what the switch would do. Changes nothing (no fetch,
                        build, task, registry, shortcut, state or log write).
                        Exit 0 = the switch can go ahead, 1 = a problem.
  (no switch)           Normal start (what the "Ayo (daily)" shortcut runs).
                        Also starts the update watcher.
  -AutoStart            What the login autostart runs: only when the daily
                        setup is active, and the window starts minimized.
  -Boot                 What the repointed boot task (the one that used to start
                        the installed app, if there was one) runs, before anyone
                        signs in: only the daemon (phone listener + tunnel), no
                        window, no fetch, no build. Stays running while that
                        daemon runs; the sign-in start replaces it.
  -Notify               Report a failure as a Windows notification (the
                        shortcuts start this script without a window).
  -InstallCli           Only the stable `ao` command: copies the built daily
                        ao.exe to <AO home>\daily\bin\ao.exe and puts that folder
                        at the front of the USER PATH (backup of the old value in
                        state.json and backup\user-path-before-daily-cli.json).
                        Starts, stops and builds nothing. Also repairs a lost
                        PATH entry or copy. rollback-to-installed-ao.ps1 undoes it.

  Every backend build (-Prepare, normal start, update-daily-ao.ps1) refreshes
  that copy; while the daily setup is active it also keeps the PATH entry.

  Safe to run again: never starts twice, refuses while the installed AO runs.
#>
[CmdletBinding()]
param(
	[switch]$Prepare,
	[switch]$SetupGitHubToken,
	[switch]$SwitchFromInstalled,
	[switch]$AutoStart,
	[switch]$Boot,
	[switch]$Notify,
	[switch]$NoWatcher,
	[switch]$CheckOnly,
	[switch]$InstallCli,
	[string]$ConfigFile,
	[int]$ReadyTimeoutSeconds = 300
)
. (Join-Path $PSScriptRoot 'daily-ao-common.ps1')

$cfg = Get-DailyConfig -ConfigFile $ConfigFile

# Read-only: before anything below creates folders, copies scripts, takes the
# lock or writes a log.
if ($CheckOnly) {
	$ok = $false
	try { $ok = Invoke-SwitchCheck $cfg (Read-DailyState $cfg) }
	catch {
		Write-Warn2 "controle afgebroken: $_"
		if ($_.InvocationInfo -and $_.InvocationInfo.ScriptName) { Write-Note "(in $(Split-Path $_.InvocationInfo.ScriptName -Leaf):$($_.InvocationInfo.ScriptLineNumber))" }
	}
	exit $(if ($ok) { 0 } else { 1 })
}

Initialize-DailyDirs $cfg
Install-DailyScripts $cfg $PSScriptRoot

# GitHub access, before the transcript starts: the token is typed into a hidden
# prompt, and nothing of that exchange belongs in a log file.
if ($Prepare -or $SetupGitHubToken) {
	try {
		Assert-GitAvailable
		if (Test-GitHubTokenSupported $cfg) { Invoke-GitHubAccessStep $cfg -Force:$SetupGitHubToken }
		elseif ($SetupGitHubToken) { throw "De repository-URL $($cfg.RepoUrl) heeft geen token nodig of ondersteunt het niet." }
	} catch {
		Write-Warn2 "$_"
		exit 1
	}
	if ($SetupGitHubToken) { Ensure-AoAppProtocol $cfg; exit 0 }
}

$transcript = Join-Path $cfg.LogDir ("start-{0}-{1}.log" -f (Get-Date -Format 'yyyyMMdd-HHmmss'), $PID)
Start-Transcript -LiteralPath $transcript | Out-Null

# Every mode repairs a lost sign-in link, also when this run ends early below.
Ensure-AoAppProtocol $cfg

# Only the stable ao command: no AO process is started, stopped or built. State
# is read and written under the update lock so a running update or watcher does
# not overwrite it.
if ($InstallCli) {
	$cliFailed = $false
	$cliLock = New-DailyMutex $cfg 'AoDailyUpdate'
	$haveCliLock = Enter-DailyMutex $cliLock 120000
	try {
		if (-not $haveCliLock) { throw 'Een update loopt nog; probeer het zo opnieuw.' }
		Write-Step "Stabiele ao-opdracht | AO-map $($cfg.AoHome)$(if ($cfg.IsSimulation) { ' | SIMULATIE' })"
		$state = Read-DailyState $cfg
		if (-not $state['active']) { Write-Warn2 'de daily setup staat niet aan (niet overgestapt of teruggezet); de ao-opdracht wordt toch geinstalleerd. Terugdraaien: rollback-to-installed-ao.ps1.' }
		$result = Sync-DailyCli $cfg
		if ($result -in 'skipped', 'failed') { throw 'er is geen stabiele ao om op de PATH te zetten' }
		Enable-DailyCliPath $cfg $state
		Write-DailyState $cfg $state
		Write-Ok 'Klaar. Nieuwe PowerShell-vensters vinden `ao` nu in de daily-map; al draaiende sessies en vensters houden hun oude PATH tot ze opnieuw starten.'
	} catch {
		Write-Warn2 "$_"
		$cliFailed = $true
	} finally {
		if ($haveCliLock) { $cliLock.ReleaseMutex() }
		Stop-Transcript | Out-Null
	}
	exit $(if ($cliFailed) { 1 } else { 0 })
}

# Never twice: a shortcut click during the login autostart just ends here. The
# lock is shared with the boot task in session 0; a sign-in start that finds
# the boot start still busy waits for it and then takes over.
$startLock = New-DailyMutex $cfg 'AoDailyStart'
$holderFile = Join-Path $cfg.DailyDir 'start-lock.json'
$mode = if ($Boot) { 'boot' } elseif ($AutoStart) { 'autostart' } elseif ($Prepare) { 'prepare' } elseif ($SwitchFromInstalled) { 'switch' } else { 'start' }
$haveLock = Enter-DailyMutex $startLock 0
if (-not $haveLock -and -not $Boot) {
	$holder = $null
	try { $holder = Get-Content -Raw -LiteralPath $holderFile | ConvertFrom-Json } catch { }
	if ($holder -and $holder.mode -eq 'boot') {
		Write-Note 'De opstart-taak start Ayo (daily) nog; wachten en dan overnemen.'
		$haveLock = Enter-DailyMutex $startLock 300000
	}
}
if (-not $haveLock) {
	Write-Note 'Er loopt al een start van Ayo (daily); deze doet niets.'
	Stop-Transcript | Out-Null
	exit 0
}
@{ mode = $mode; pid = $PID; session = (Get-CurrentSessionId); at = (Get-Date).ToString('o') } | ConvertTo-Json | Set-Content -LiteralPath $holderFile -Encoding utf8
$script:lockHeld = $true
function Exit-StartLock {
	if (-not $script:lockHeld) { return }
	Remove-Item -LiteralPath $holderFile -Force -ErrorAction SilentlyContinue
	$startLock.ReleaseMutex()
	$script:lockHeld = $false
}

# The watcher applies UI-only merges by itself and announces the rest.
function Start-UpdateWatcher {
	$updater = Join-Path $cfg.ScriptsDir 'update-daily-ao.ps1'
	$watching = Get-ProcessTable | Where-Object { $_.CommandLine -and $_.CommandLine.Contains($updater) -and $_.CommandLine -match '-Watch' }
	if ($watching) { return }
	$pwsh = Get-PwshPath $cfg
	$argLine = "--headless `"$pwsh`" -NoProfile -ExecutionPolicy Bypass -File `"$updater`" -Watch"
	if ($cfg.ConfigFile) { $argLine += " -ConfigFile `"$($cfg.ConfigFile)`"" }
	Disable-StdHandleInheritance
	$w = Start-Process -FilePath (Join-Path $env:SystemRoot 'System32\conhost.exe') -ArgumentList $argLine -PassThru
	$state['watcherPid'] = $w.Id
	Write-DailyState $cfg $state
	Write-Ok "update-watcher gestart (PID $($w.Id)); UI-updates verschijnen vanzelf"
}

$script:startFailed = $false
try {
	$state = Read-DailyState $cfg
	Write-Step "AO daily | AO-map $($cfg.AoHome) | poort $($cfg.Port) | Windows-sessie $(Get-CurrentSessionId)$(if ($cfg.IsSimulation) { ' | SIMULATIE' }) | $mode"

	if (($AutoStart -or $Boot) -and -not $state['active']) {
		Write-Note 'De daily setup is niet actief (niet overgestapt of teruggezet); deze start doet niets.'
		return
	}

	# --- Boot task: the daemon alone, before anyone signs in ------------------------
	if ($Boot) {
		if (@((Get-DailyProcesses $cfg).ElectronMain).Count -gt 0) { Write-Ok 'Ayo (daily) draait al; de opstart-taak doet niets.'; return }
		$existing = Get-CurrentDaemon $cfg
		if ($existing) { Write-Ok "Er draait al een AO-daemon ($($existing.Kind), PID $($existing.Pid)); de opstart-taak doet niets."; return }
		Sync-DesktopCredentials $cfg
		$headless = Start-DailyHeadlessDaemon $cfg
		Write-Ok "daemon PID $($headless.Daemon.Pid) op poort $($headless.Daemon.Port) ($($headless.Daemon.Exe))"
		$state['lastBoot'] = @{ at = (Get-Date).ToString('o'); daemonPid = $headless.Daemon.Pid; session = (Get-CurrentSessionId); exe = $headless.Daemon.Exe }
		Write-DailyState $cfg $state
		Exit-StartLock
		# Keep running while this daemon runs, as the installed app did: Windows
		# then keeps this logon (profile, saved credentials) loaded for the daemon
		# and its agents, and the task's next action waits as before.
		$headless.Process.WaitForExit()
		Write-Ok "daemon $($headless.Daemon.Pid) is gestopt of bij het aanmelden overgenomen; de opstart-taak is klaar."
		return
	}
	if (-not $Prepare -and (Get-CurrentSessionId) -eq 0) {
		throw "Deze Windows-sessie heeft geen bureaublad (sessie 0, gestart voor het aanmelden). Start Ayo (daily) vanuit je eigen sessie, of laat de taak '$($cfg.FrontendTaskName)' het doen."
	}

	# --- What is running now? -------------------------------------------------
	$running = Get-DailyProcesses $cfg
	if (-not $Prepare -and @($running.ElectronMain).Count -gt 0) {
		Write-Ok "AO daily draait al (Electron PID $($running.ElectronMain[0].ProcessId))."
		if (-not $NoWatcher) { Start-UpdateWatcher }
		return
	}
	$daemon = Get-CurrentDaemon $cfg
	$installedMains = Get-InstalledMainProcesses $cfg $daemon
	if (-not $Prepare) {
		if ($daemon -and $daemon.Kind -eq 'other') {
			throw "Een andere AO-daemon ($($daemon.Exe)) gebruikt $($cfg.RunFile). Stop die eerst."
		}
		$installedUp = ($daemon -and $daemon.Kind -eq 'installed') -or @($installedMains).Count -gt 0
		if ($installedUp -and -not $SwitchFromInstalled) {
			throw 'De geinstalleerde AO draait nog. Eenmalig overstappen: start-daily-ao.ps1 -SwitchFromInstalled'
		}
		if ($SwitchFromInstalled -and @($installedMains).Count -gt 0 -and -not ($daemon -and $daemon.Kind -eq 'installed')) {
			throw 'De geinstalleerde app draait zonder daemon. Sluit hem en start opnieuw.'
		}
	}

	# --- Toolchain, checkout, deps, builds -------------------------------------
	Initialize-Checkout $cfg
	$goRoot = Install-GoToolchain $cfg
	if ($Prepare -or $SwitchFromInstalled) { [void](Install-PortablePwsh $cfg) }
	$nodeDir = Install-NodeToolchain $cfg
	Write-Ok "Node $(& (Join-Path $nodeDir 'node.exe') -v) ($nodeDir)"
	Set-DailyEnv $cfg $goRoot $nodeDir
	if (@($running.ElectronMain).Count -gt 0 -and $Prepare) {
		Write-Note 'AO daily draait; npm ci wordt overgeslagen (bestanden in gebruik).'
	} else {
		Install-FrontendDeps $cfg $state
	}
	Update-Builds $cfg $state
	$head = (Invoke-Git $cfg @('rev-parse', 'HEAD')).Trim()
	$state['preparedCommit'] = $head
	Write-DailyState $cfg $state
	if ($Prepare) {
		Write-Ok "Klaar voor de overstap. Checkout $($cfg.Checkout) op $($head.Substring(0, 12))."
		return
	}

	# --- Switch or restart bookkeeping ------------------------------------------
	$before = @()
	if ($SwitchFromInstalled) {
		# Every detection step first: a problem stops the switch before any change.
		if (-not (Invoke-SwitchCheck $cfg $state -Preflight)) { throw 'De controle voor de overstap vond een probleem (zie LET OP hierboven); er is niets veranderd.' }
		# Login items first (may need one UAC prompt for a boot task); if that
		# fails, the installed AO is still running untouched.
		Enable-DailyAutostart $cfg $state
		if ($daemon -and $daemon.Kind -eq 'installed') {
			$state['switchedFromVersion'] = Get-InstalledVersion $cfg
			$before = Stop-InstalledForSwitch $cfg $state
		} else {
			Write-Note 'Geen geinstalleerde AO actief; alleen snelkoppelingen en autostart worden omgezet.'
		}
		Disable-InstalledShortcuts $cfg $state
		$state['active'] = $true
		$state['switchedAt'] = (Get-Date).ToString('o')
		Write-DailyState $cfg $state
		# Update-Builds ran before the setup was active: now the stable ao gets
		# its place on the user PATH (a failure only warns; -InstallCli repairs).
		Update-DailyCli $cfg $state
		Write-DailyState $cfg $state
	} elseif ($daemon -and $daemon.Kind -eq 'daily') {
		# A kept-alive daily daemon from an earlier run. The new Electron replaces
		# it (gracefully); compare sessions afterwards.
		$before = Save-SessionSnapshot $cfg $daemon.Port 'before-start' $state
	}
	if ($state['active'] -and -not (Test-Path -LiteralPath (Get-AutostartLinkPath $cfg))) {
		Enable-DailyAutostart $cfg $state
		Write-DailyState $cfg $state
	}
	if (@($before).Count -eq 0 -and $state['pendingSessions'] -and -not $daemon) {
		# An earlier run stopped a daemon but never finished: check those sessions.
		$before = @($state['pendingSessions'])
		Write-Note "sessies van een onderbroken eerdere run worden gecontroleerd: $($before -join ', ')"
	}

	Sync-DesktopCredentials $cfg

	# --- Launch -----------------------------------------------------------------
	Write-Step 'AO daily starten (Electron + Vite, verborgen; log in daily\logs)'
	$launch = Start-DailyFrontend $cfg $goRoot $nodeDir
	$state['forgePid'] = $launch.ForgePid
	$state['startedCommit'] = $head
	$state['startedAt'] = (Get-Date).ToString('o')
	Write-DailyState $cfg $state
	try {
		$ready = Wait-DaemonReady $cfg $launch.StartedUtc 'daily' $ReadyTimeoutSeconds
	} catch {
		Write-Warn2 'AO werd niet op tijd klaar. Laatste regels van de log:'
		if (Test-Path -LiteralPath $launch.Log) { Get-Content -LiteralPath $launch.Log -Tail 25 | ForEach-Object { Write-Note $_ } }
		if (Test-Path -LiteralPath $launch.ErrLog) { Get-Content -LiteralPath $launch.ErrLog -Tail 25 | ForEach-Object { Write-Note $_ } }
		throw
	}
	Write-Ok "daemon PID $($ready.Pid) op 127.0.0.1:$($ready.Port) ($($ready.Exe))"
	$electron = (Get-DailyProcesses $cfg).ElectronMain | Select-Object -First 1
	if ($electron) { Write-Ok "Electron PID $($electron.ProcessId)" }
	if ($AutoStart) { [void](Hide-DailyWindows $cfg) }

	# --- Sessions ----------------------------------------------------------------
	$aoExe = Get-DaemonManifestPath $cfg
	$restored = Confirm-SessionsSurvived $cfg $ready.Port $before $aoExe
	$state['lastStart'] = @{ at = (Get-Date).ToString('o'); daemonPid = $ready.Pid; sessionsBefore = @($before).Count; restored = @($restored); autoStart = [bool]$AutoStart }
	$state['pendingSessions'] = @()
	Write-DailyState $cfg $state

	if (-not $NoWatcher) { Start-UpdateWatcher }
	Write-Ok "AO daily draait op commit $($head.Substring(0, 12))."
} catch {
	Write-Warn2 "$_"
	if ($_.InvocationInfo -and $_.InvocationInfo.ScriptName) { Write-Note "(in $(Split-Path $_.InvocationInfo.ScriptName -Leaf):$($_.InvocationInfo.ScriptLineNumber))" }
	if ($SwitchFromInstalled) {
		try {
			$after = Read-DailyState $cfg
			$items = $after['installedLoginItems']
			if ($after['active'] -or ($items -and (@($items.tasks | Where-Object { $_ }).Count + @($items.runValues | Where-Object { $_ }).Count + @($items.startupLinks | Where-Object { $_ }).Count) -gt 0)) {
				Write-Warn2 'De overstap is half gelukt. Afmaken: draai dezelfde opdracht opnieuw (wat al klaar is, wordt overgeslagen). Alles terugzetten: rollback-to-installed-ao.ps1'
			}
		} catch { }
	}
	if (($Notify -or $AutoStart) -and -not $cfg.IsSimulation -and (Get-CurrentSessionId) -ne 0) { Show-Notice "Ayo (daily) is niet gestart: $($_.Exception.Message) Log: $transcript" }
	if ($Boot) { try { $s = Read-DailyState $cfg; $s['lastBootError'] = @{ at = (Get-Date).ToString('o'); error = "$_" }; Write-DailyState $cfg $s } catch { } }
	$script:startFailed = $true
} finally {
	# Again: this run may have switched the daily setup on or installed Electron.
	Ensure-AoAppProtocol $cfg
	Exit-StartLock
	Stop-Transcript | Out-Null
}
if ($script:startFailed) { exit 1 }
