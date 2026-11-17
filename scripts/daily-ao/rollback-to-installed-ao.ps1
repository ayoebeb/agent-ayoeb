<#
.SYNOPSIS
  Goes back from the daily AO to the installed Orchestrator.inc app, keeping
  every session running.

.DESCRIPTION
  1. Checks the installed app is there and which database migrations the daily
     build added that the installed version does not know.
  2. Stops the daily daemon gracefully (agents keep running) and the daily
     Electron, and the update watcher.
  0. First puts back the scheduled tasks the switch changed: the boot task
     (a task that started the installed app at boot, if there was one) gets its
     exact original definition (one UAC prompt, only if you may not change it).
  3. Copies the newest cloud sign-in / saved remotes back, puts the original
     shortcuts and login items back, takes the stable daily `ao` off the user
     PATH (the old PATH comes back exactly) and deletes <daily>\bin.
  4. Starts the installed app; it adopts the running sessions. Sessions that do
     not come back are restored.
  The daily checkout and tools stay, so start-daily-ao.ps1 -SwitchFromInstalled
  switches back again.
#>
[CmdletBinding()]
param(
	[switch]$Yes,
	[string]$ConfigFile,
	[int]$ReadyTimeoutSeconds = 180
)
. (Join-Path $PSScriptRoot 'daily-ao-common.ps1')

$cfg = Get-DailyConfig -ConfigFile $ConfigFile
Initialize-DailyDirs $cfg
$transcript = Join-Path $cfg.LogDir ("rollback-{0}.log" -f (Get-Date -Format 'yyyyMMdd-HHmmss'))
Start-Transcript -LiteralPath $transcript | Out-Null
try {
	$state = Read-DailyState $cfg
	$version = Get-InstalledVersion $cfg
	if (-not $version) { throw "Geen geinstalleerde app gevonden in $($cfg.InstalledDir). Installeer Orchestrator.inc opnieuw via GitHub Releases en draai dit script daarna nog eens." }
	Write-Step "Terug naar de geinstalleerde Orchestrator.inc $version$(if ($cfg.IsSimulation) { ' (SIMULATIE)' })"

	# --- Database check -----------------------------------------------------------
	# Migrations are embedded in ao.exe by file name. Any migration in the daily
	# checkout that the installed binary lacks has already been applied to the
	# database; the older binary will run against that newer schema.
	$migrationsDir = Join-Path $cfg.Checkout 'backend\internal\storage\sqlite\migrations'
	$newer = @()
	if (Test-Path -LiteralPath $migrationsDir) {
		$text = [Text.Encoding]::ASCII.GetString([IO.File]::ReadAllBytes($cfg.InstalledAo))
		$known = [regex]::Matches($text, '\d{17}_[a-z0-9_]{3,80}\.sql') | ForEach-Object Value | Sort-Object -Unique
		$maxKnown = ($known | ForEach-Object { [int64]($_.Substring(0, 17)) } | Measure-Object -Maximum).Maximum
		# Older migrations use short numbers (0001_...); only the leading digits count.
		$newer = @(Get-ChildItem -LiteralPath $migrationsDir -Filter '*.sql' |
			Where-Object { $_.Name -match '^(\d+)_' -and [int64]$Matches[1] -gt $maxKnown } | ForEach-Object Name)
	}
	if (@($newer).Count -gt 0) {
		Write-Warn2 "De daily build heeft $(@($newer).Count) databasewijziging(en) gedaan die versie $version niet kent:"
		$newer | ForEach-Object { Write-Note "  $_" }
		Write-Note 'Oudere versies negeren onbekende migraties. Nieuwe tabellen/kolommen/indexen zijn dan geen probleem;'
		Write-Note 'een migratie die iets hernoemt of verwijdert kan dat wel zijn. Kijk bij twijfel eerst in die bestanden.'
		Write-Note 'Na de terugstap werkt de geinstalleerde app zichzelf bij; een nieuwere release kent deze migraties wel.'
	} else {
		Write-Ok "geen databasewijzigingen die versie $version niet kent"
	}
	# Both apps share the Electron profile (<AO home>\electron). A newer Chromium
	# in the daily build may have upgraded it; an older one may not read it all.
	$installedElectron = (Get-Content -LiteralPath (Join-Path $cfg.InstalledDir 'version') -ErrorAction SilentlyContinue | Select-Object -First 1)
	$dailyElectronPkg = Join-Path $cfg.Frontend 'node_modules\electron\package.json'
	$dailyElectron = if (Test-Path -LiteralPath $dailyElectronPkg) { (Get-Content -Raw -LiteralPath $dailyElectronPkg | ConvertFrom-Json).version } else { $null }
	if ($installedElectron -and $dailyElectron -and ([version]$dailyElectron -gt [version]$installedElectron)) {
		Write-Warn2 "De daily build gebruikt Electron $dailyElectron, de geinstalleerde app $installedElectron. Cookies/inlog in de ingebouwde browser kunnen na de terugstap opnieuw nodig zijn."
	} elseif ($installedElectron) {
		Write-Ok "zelfde of oudere Electron in de daily build ($dailyElectron vs $installedElectron)"
	}
	if ((Get-CurrentSessionId) -eq 0) { throw 'Dit proces draait zonder bureaublad (sessie 0, gestart voor het aanmelden); de app zou onzichtbaar starten. Draai de rollback in je eigen PowerShell-venster.' }
	if (-not $Yes) {
		$answer = Read-Host 'Terug naar de geinstalleerde app? Lopende sessies blijven doorgaan. (j/n)'
		if ($answer -notmatch '^(j|ja|y|yes)$') { Write-Note 'Afgebroken; er is niets veranderd.'; return }
	}

	# --- Scheduled tasks first --------------------------------------------------------
	# Needs one UAC prompt (and maybe the account password) for a task like
	# a boot task the switch repointed; if that is refused, nothing else has changed yet.
	Write-Step 'Geplande taken terugzetten zoals ze voor de overstap waren'
	Restore-InstalledTasks $cfg $state

	# --- Stop the daily AO -------------------------------------------------------
	$before = @()
	$daemon = Get-CurrentDaemon $cfg
	if ($daemon -and $daemon.Kind -eq 'installed') {
		Write-Ok 'De geinstalleerde AO draait al.'
	} else {
		if ($daemon -and $daemon.Kind -eq 'other') { throw "Een onbekende AO-daemon ($($daemon.Exe)) draait; stop die eerst." }
		if ($daemon) {
			$before = Save-SessionSnapshot $cfg $daemon.Port 'before-rollback' $state
			Write-Step "Daily daemon (PID $($daemon.Pid)) netjes stoppen; sessies blijven lopen"
			Stop-DaemonGracefully $cfg $daemon
		}
		Write-Step 'Daily Electron en update-watcher stoppen'
		$updater = Join-Path $cfg.ScriptsDir 'update-daily-ao.ps1'
		Get-ProcessTable | Where-Object { $_.CommandLine -and $_.CommandLine.Contains($updater) -and $_.CommandLine -match '-Watch' } |
			ForEach-Object { Stop-ExactProcess $cfg ([int]$_.ProcessId) 'update-watcher' }
		Stop-DailyFrontend $cfg
	}

	Sync-DesktopCredentials $cfg
	Restore-InstalledShortcuts $cfg $state
	# No more Ayo (daily) at login; the installed app's own login item (if it
	# had one) comes back.
	Disable-DailyAutostart $cfg $state
	$state['active'] = $false
	Write-DailyState $cfg $state

	# The stable daily `ao` goes too: its entry leaves the user PATH (the old PATH
	# comes back exactly) and the bin folder is deleted. A problem here must not
	# stop the rollback; it is reported, and the folder is harmless once it is off
	# the PATH.
	Write-Step 'Stabiele daily ao van de PATH halen'
	try { Disable-DailyCli $cfg $state; Write-DailyState $cfg $state }
	catch { Write-Warn2 "de daily ao van de gebruikers-PATH halen mislukte: $_ Backup van de oude PATH: $(Get-DailyCliBackupFile $cfg)" }

	# --- Start the installed app ---------------------------------------------------
	if (-not ($daemon -and $daemon.Kind -eq 'installed')) {
		Write-Step 'Geinstalleerde app starten'
		Set-InstalledEnv $cfg
		$since = (Get-Date).ToUniversalTime()
		Disable-StdHandleInheritance
		Start-Process -FilePath $cfg.InstalledExe -WorkingDirectory $cfg.InstalledDir | Out-Null
		$ready = Wait-DaemonReady $cfg $since 'installed' $ReadyTimeoutSeconds
		Write-Ok "geinstalleerde daemon PID $($ready.Pid) op poort $($ready.Port)"
		$restored = Confirm-SessionsSurvived $cfg $ready.Port $before $cfg.InstalledAo
		$state['rolledBackAt'] = (Get-Date).ToString('o')
		$state['lastRollback'] = @{ version = $version; sessionsBefore = @($before).Count; restored = @($restored); newerMigrations = $newer }
		$state['pendingSessions'] = @()
		Write-DailyState $cfg $state
	}
	Write-Ok "Terug op de geinstalleerde Orchestrator.inc $version. Opnieuw overstappen: start-daily-ao.ps1 -SwitchFromInstalled"
} finally {
	Stop-Transcript | Out-Null
}
