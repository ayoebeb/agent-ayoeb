# Ayo (daily) op een tweede pc: Orchestrator vanaf `main`

In plaats van de geïnstalleerde Orchestrator.inc draait die pc je eigen `main` (alleen gereviewde, gemergde code), met dezelfde data en sessies. Eerst voorbereiden (de huidige AO blijft draaien), later in één keer overstappen. Terugdraaien kan altijd.

**Nodig:** *Git for Windows* (`git --version`; anders `winget install --id Git.Git -e`, nieuw PowerShell-venster) en een eigen alleen-lezen GitHub-token (stap 1), zodat `gh auth switch` niets stukmaakt. Node 24, Go en PowerShell 7 zet het script zelf klaar in `~/.ao/tools` (checksum gecontroleerd, niets systeembreed).

**1. Token maken** (eenmalig, in een browser waar je als `ayoebeb` bent ingelogd, niet als werkaccount)
<https://github.com/settings/personal-access-tokens/new> (of: profielfoto → Settings → Developer settings → Personal access tokens → Fine-grained tokens → Generate new token). Resource owner `ayoebeb`; Repository access *Only select repositories* → `agent-ayoeb`; Repository permissions **Contents → Read-only** (het enige recht); kies een looptijd (max. 1 jaar); *Generate token* en kopieer `github_pat_…`.

**2. Token opslaan en clonen** (zelf doen; plak het token nooit in een chat met een agent)

```powershell
$t = Read-Host 'Plak het token (je ziet niets)' -AsSecureString
$p = [System.Net.NetworkCredential]::new('', $t).Password
$gcm = '-c','credential.helper=','-c','credential.helper=manager','-c','credential.useHttpPath=true','-c','credential.provider=generic'
"x=1`nprotocol=https`nhost=github.com`npath=ayoebeb/agent-ayoeb.git`nusername=ayoebeb`npassword=$p`n`n" | git @gcm credential approve   # x=1: Git negeert die regel; vangt een onzichtbaar teken van sommige consoles op
$p = $null
git @gcm clone https://github.com/ayoebeb/agent-ayoeb.git "$HOME\.ao\daily\agent-ayoeb"
```

Het token staat nu in Windows-referentiebeheer, alleen voor die ene repository-URL. **De bestaande git- en gh-aanmeldingen van die pc blijven onaangeraakt**, ook die voor de Yoobi-code (waar die ook staat): de scripts doen geen `gh auth`, zetten niets in de globale git-instellingen en wijzigen of vervangen geen bestaande Credential Manager-gegevens. Er komt één nieuw item bij (`git:https://github.com/ayoebeb/agent-ayoeb.git`) plus instellingen in de daily-checkout zelf, alleen voor die URL. Andere repositories en code-hosts merken er niets van.

**3. Voorbereiden** (10 tot 20 minuten; raakt de draaiende AO niet aan). Controleert het token (leesrecht ja, schrijfrecht nee), zet tools klaar, kopieert de scripts naar `~/.ao/daily/scripts`, `npm ci`, bouwt de backend.

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File "$HOME\.ao\daily\agent-ayoeb\scripts\daily-ao\start-daily-ao.ps1" -Prepare
```

Geeft `pwsh` "not recognized"? Run eerst `powershell -NoProfile -ExecutionPolicy Bypass -File "$HOME\.ao\daily\agent-ayoeb\scripts\daily-ao\bootstrap-pwsh.ps1"`; die zet een losse PowerShell 7 klaar en toont de opdracht voor stap 3.

**4. Overstappen, op een rustig moment** (ongeveer 2 minuten; agents lopen door, de daemon is ongeveer 1 minuut weg). Eerst alleen kijken kan met `-CheckOnly` erachter: dat verandert niets, en staat er **LET OP** in, dan stopt de echte overstap ook.

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File "$HOME\.ao\daily\scripts\start-daily-ao.ps1" -SwitchFromInstalled
```

Het script stopt de geïnstalleerde AO netjes (agents blijven draaien), zet autostart aan, start Ayo (daily) en controleert dat alle sessies nog lopen. Controle: venstertitel **Ayo (dev)**, oranje taakbalkicoon, je sessies staan erin. Vraagt Windows Firewall om toegang voor `ao.exe`: **Toestaan** (privénetwerk).

**De UAC-vraag.** Meestal komt er geen: alleen als op die pc een geplande taak de geïnstalleerde app bij het opstarten start én jij die taak niet zelf mag aanpassen. Komt hij wel: een Windows-venster voor **PowerShell, uitgever Microsoft Corporation**: klik **Ja**. Er opent kort een venster **"Ayo (daily) - geplande taak aanpassen"** dat alleen de actie van die taak omzet (trigger, account en andere acties blijven gelijk). Vraagt het om een wachtwoord, typ dan het wachtwoord van je Windows-account (werkaccount: je werkwachtwoord; je pincode werkt niet). **Nee** of leeg laten stopt het script zonder iets te veranderen; heb je geen beheerdersrechten, klik dan **Nee** en vraag IT.

**Terugdraaien:** `pwsh -NoProfile -ExecutionPolicy Bypass -File "$HOME\.ao\daily\scripts\rollback-to-installed-ao.ps1"`, antwoord `j`. Sessies lopen door, oude snelkoppelingen en taken komen exact terug; checkout, tools en token blijven staan.

**Later:** updates met `pwsh -NoProfile -File "$HOME\.ao\daily\scripts\update-daily-ao.ps1"` (UI-wijzigingen komen vanzelf; een herstart pas na jouw "j"). Token verlopen ("GitHub weigert de toegang")? Maak een nieuw token (stap 1) en run `start-daily-ao.ps1 -SetupGitHubToken`.

**De Yoobi-orchestrator mag helpen:** de orchestrator op die pc kan stap 3 en 4 (en later updates en rollback) voor je uitvoeren; het zijn gewone PowerShell-opdrachten. Voor jou blijven het token (stap 1 en 2, niet in de chat) en eventuele UAC- of wachtwoordvensters bij stap 4, die op jouw scherm verschijnen.
