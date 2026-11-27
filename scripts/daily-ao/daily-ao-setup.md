# Ayo (daily): AO draaien vanaf `main`

Kort stappenplan voor een tweede pc: [`tweede-pc-handleiding.nl.md`](tweede-pc-handleiding.nl.md). Dit document is het naslagwerk.

## Wat verandert er

- Je gebruikt niet meer de geïnstalleerde Orchestrator.inc, maar je eigen `main` (alleen gereviewde, gemergde code).
- Zelfde data, zelfde sessies, zelfde instellingen, cookies en browserprofielen, dezelfde poort en dezelfde remote (telefoon + tunnel) als de geïnstalleerde app.
- **Na elke herstart van de pc** (ook 's nachts door Windows Update) start Ayo (daily) binnen ongeveer een minuut zonder venster, als er een geplande taak was die de geïnstalleerde app bij het opstarten startte. Je telefoon kan dan bij AO, nog voordat je je aanmeldt. Zonder zo'n taak start Ayo (daily) bij het aanmelden.
- Zodra je je aanmeldt, opent Ayo (daily) geminimaliseerd en neemt het over. Je sessies lopen door.
- UI-wijzigingen zie je binnen ongeveer 30 seconden na een merge, zonder herstart.
- Een herstart (Electron of backend) gebeurt pas na jouw "ja". Je sessies lopen daarbij gewoon door.
- De scripts zelf komen uit deze repository (`scripts/daily-ao`). Na een merge die ze wijzigt, werkt `update-daily-ao.ps1` de kopie in `~/.ao/daily/scripts` bij (alleen als alle scripts in de checkout zonder syntaxfout inlezen).
- Alles staat in `~/.ao`: de checkout in `~/.ao/daily`, Node, Go en een losse PowerShell in `~/.ao/tools`. Er wordt niets systeembreed geïnstalleerd. De enige aanpassing buiten `~/.ao` is één ingang vooraan in je **gebruikers-PATH** (alleen jouw account), zodat `ao` in een nieuw PowerShell-venster de daily-versie is. Zie "De `ao`-opdracht".

## Wat je nodig hebt

| Wat | Waarom | Als het ontbreekt |
| --- | --- | --- |
| **Git for Windows** (met Git Credential Manager) | de checkout ophalen en bijhouden | Het script stopt met uitleg. Installeren: <https://git-scm.com/download/win> of `winget install --id Git.Git -e`. Wordt niet automatisch geïnstalleerd. |
| **PowerShell 7.4 of nieuwer** (`pwsh`) | de scripts draaien erin | `powershell -NoProfile -ExecutionPolicy Bypass -File bootstrap-pwsh.ps1` zet een losse kopie in `~/.ao/tools` (checksum gecontroleerd). |
| **Node 24** (de versie van CI) | frontend bouwen | Wordt als losse kopie in `~/.ao/tools/node-v24.x.y-win-x64` gezet (nodejs.org, SHA-256 uit `SHASUMS256.txt`). Een Node 24 van fnm of op de PATH wordt eerst gebruikt. |
| **Go** (de versie uit `backend/go.mod`) | backend bouwen | Wordt als losse kopie in `~/.ao/tools/go-<versie>` gezet (go.dev, SHA-256 van go.dev). |
| **Toegang tot de privé-repository** | `main` ophalen | Een eigen alleen-lezen token, zie "GitHub-toegang". |

Een download waarvan de checksum niet klopt, wordt verwijderd en nooit gebruikt.

## GitHub-toegang (los van je actieve `gh`-account)

De daily-checkout haalt één privé-repository op (`ayoebeb/agent-ayoeb`). Zonder eigen inlog gebruikt Git daarvoor wat `gh` of Git Credential Manager op dat moment aanbiedt: bij `gh auth switch` naar een ander account (bijvoorbeeld een werkaccount zonder toegang) stopt het ophalen van updates.

De oplossing is een **fine-grained token met alleen leesrechten voor alleen deze repository**:

- Aanmaken: <https://github.com/settings/personal-access-tokens/new> (ingelogd als `ayoebeb`). Resource owner `ayoebeb`; Repository access: *Only select repositories* → `agent-ayoeb`; Repository permissions: **Contents: Read-only**. `Metadata: Read-only` komt er automatisch bij. Verder niets aanzetten. Kies zelf de looptijd (maximaal 1 jaar).
- Opslaan: `start-daily-ao.ps1 -Prepare` leidt je erdoorheen (of alleen die stap: `start-daily-ao.ps1 -SetupGitHubToken`). Je plakt het token in een verborgen prompt. Het script controleert eerst of het token deze repository kan lezen en of het **niet** kan schrijven (een token met schrijfrechten wordt geweigerd), en slaat het dan op in Windows-referentiebeheer onder `git:https://github.com/ayoebeb/agent-ayoeb.git`: alleen voor die ene URL (`credential.useHttpPath`), dus geen andere GitHub-repository krijgt het token.
- In de daily-checkout staat daarna een instelling per URL: de helper-lijst wordt leeggemaakt en alleen Git Credential Manager blijft over. Daardoor wint de `gh`-helper niet meer, ook niet na `gh auth switch`.
- **Bestaande aanmeldingen blijven onaangeraakt.** Op een pc die al git of `gh` gebruikt voor andere code (bijvoorbeeld een werkgever-code-host) doen de scripts geen `gh auth`, geen `git config --global` en geen wijziging of vervanging van een bestaande Credential Manager-aanmelding. Er komt precies één nieuw item bij, genoemd naar de URL, en een instelling in de daily-checkout zelf, alleen voor die URL. De scripts testen de gewone aanmelding van de pc ook nooit met een eigen git-opdracht: na een gewone fetch laat git elke geconfigureerde helper de aanmelding opslaan of wissen, en dat zou die van iets anders kunnen overschrijven. Dit is met een simulatie bewaakt (`sim-github-token.ps1`: globale git-config en andere Credential Manager-items blijven byte voor byte gelijk).
- Het token komt nooit in een bestand, logboek of opdrachtregel. Plak het alleen in dat prompt, nooit in een chat met een agent.
- Verlopen of ingetrokken: het ophalen meldt "GitHub weigert de toegang" met de opdracht om te herstellen: `start-daily-ao.ps1 -SetupGitHubToken`. Git laat Credential Manager het afgewezen token zelf vergeten.
- Verwijderen: Windows-referentiebeheer → Windows-referenties → `git:https://github.com/ayoebeb/agent-ayoeb.git`, en intrekken op github.com bij Settings → Developer settings.

## Voorbereiden (eenmalig, ongeveer 10 tot 20 minuten, de geïnstalleerde AO blijft gewoon draaien)

1. Clone de repository naar `~/.ao/daily/agent-ayoeb` (de stappen met het token staan in de handleiding voor de tweede pc).
2. `pwsh -NoProfile -ExecutionPolicy Bypass -File "$HOME\.ao\daily\agent-ayoeb\scripts\daily-ao\start-daily-ao.ps1" -Prepare`
   - controleert Git en GitHub-toegang (en leidt je naar het token als dat nodig is),
   - zet Node, Go en een losse PowerShell klaar in `~/.ao/tools` als ze ontbreken,
   - kopieert de scripts naar `~/.ao/daily/scripts` (daar wijzen snelkoppelingen en taken naar),
   - draait `npm ci` en bouwt de backend. Raakt de draaiende AO niet aan.

## Overstappen (eenmalig, op een rustig moment, ongeveer 2 minuten)

1. Zorg dat geen agent net iets belangrijks afrondt. Agents lopen door, maar de daemon is ongeveer 1 minuut weg.
2. Run in PowerShell:
   `pwsh -File "$HOME\.ao\daily\scripts\start-daily-ao.ps1" -SwitchFromInstalled`
   - Het script controleert eerst alles (**Controle overstap**) en laat zien wat het gaat veranderen. Ziet het een probleem (**LET OP**), dan stopt het en verandert er niets.
   - Eerst alleen kijken? Zet `-CheckOnly` achter de opdracht. Dan verandert er niets.
3. **Windows vraagt alleen toestemming (UAC) als een geplande taak de geïnstalleerde app bij het opstarten start en jij die taak niet zelf mag aanpassen.** Zonder zo'n taak komt er geen UAC.
   - Komt hij wel: Windows vraagt één keer toestemming voor **PowerShell** van Microsoft Corporation. Klik **Ja**. (Heb je geen beheerdersrechten, dan vraagt Windows om beheerdersgegevens: klik dan **Nee**. Er verandert niets en je kunt de overstap zonder die taak later opnieuw proberen of IT vragen.)
   - Er opent kort een venster **"Ayo (daily) - geplande taak aanpassen"**. Dat past in die taak alleen de actie aan die de oude app start. Trigger, account, opgeslagen wachtwoord en alle andere acties blijven gelijk.
   - Vraagt dat venster om een wachtwoord? Typ het wachtwoord van dit Windows-account: je Microsoft-account, of bij een werk- of schoolaccount je werkwachtwoord (je pincode werkt hier niet). Dat gebeurt alleen als Windows het opgeslagen wachtwoord niet zelf hergebruikt.
   - Klik je Nee, of laat je het wachtwoord leeg, dan stopt het script en verandert er niets.
4. Het script stopt daarna de geïnstalleerde AO netjes en sluit hem zonder een klaarstaande update te installeren. Het zet de oude snelkoppelingen apart, zet de autostart van Ayo (daily) aan, start Ayo (daily) en controleert dat al je sessies nog lopen.
5. Controleer het venster: titel **Ayo (dev)** en een oranje taakbalkicoon. Je sessies staan erin. Vraagt Windows Firewall of `ao.exe` toegang mag hebben? Kies **Toestaan** (privénetwerk). Ben je uitgelogd bij Cloud? Log één keer opnieuw in.

Daarna start Ayo (daily) vanzelf: bij het aanmelden met venster (en bij het opstarten van de pc zonder venster als er zo'n opstart-taak was). Met de hand starten kan via de snelkoppeling **Ayo (daily)**.

## Hoe updates binnenkomen

- **Alleen UI**: verschijnt vanzelf binnen ongeveer 30 seconden. Je hoeft niets te doen.
- **Electron, backend of npm-pakketten**: je krijgt een melding "AO-update klaar". Zeg tegen de orchestrator "ja, herstart", of run `update-daily-ao.ps1` en antwoord `j`. Dat duurt 20–60 seconden en je sessies lopen door.
- Komt een sessie toch niet terug, dan probeert het script die automatisch te herstellen.
- Ben je niet aangemeld, dan wordt de update wel klaargezet, maar start de nieuwe versie pas als je je aanmeldt.
- Elke update-ronde zet ook gewijzigde scripts uit `scripts/daily-ao` over naar `~/.ao/daily/scripts`. Een script dat al draait (de update-watcher) gebruikt zijn oude code tot hij opnieuw start.

## De `ao`-opdracht (CLI)

De daily-backend draait uit een map die bij elke backend-build een nieuwe naam krijgt (`~/.ao/daily/agent-ayoeb/frontend/daemon/dev-<tijd>-<pid>/ao.exe`). Daarom is er een vaste kopie.

- **Waar**: `~/.ao/daily/bin/ao.exe`, een echte `.exe` (geen shim). Die map staat **vooraan in je gebruikers-PATH**. Een nieuw PowerShell-venster vindt `ao` dus daar. Controle: `(Get-Command ao).Source` en `ao version` (toont de commit van de daily-build).
- **Hoe hij meeloopt**: elke backend-build ververst de kopie (`update-daily-ao.ps1`, `start-daily-ao.ps1` en `-Prepare`). Draait er op dat moment nog een `ao.exe` uit die map, dan wordt hij opzij gezet (Windows laat hem niet overschrijven) en bij een volgende run opgeruimd. Mislukt het verversen, dan gaat de update zelf gewoon door.
- **Waar `ao` verder vandaan komt**: de app voegt `ao` op Windows niet toe aan de PATH. Een sessie in AO krijgt zijn `ao` van de daemon die de sessie startte: die zet zijn **eigen** map vooraan. Sessies die de geïnstalleerde daemon startte (vóór de overstap) hebben daarom nog de oude `ao`. Sessies die de daily-daemon start, krijgen de daily-`ao`. De vaste map zit ook in de PATH die de daily-daemon meekrijgt (vanaf de eerstvolgende herstart van Ayo), direct achter zijn eigen map. Zo vindt een sessie `ao` ook als die oude `dev-*`-map is opgeruimd (er blijven er 5 over).
- **Wat niet meeverandert**: al draaiende sessies, en vensters of terminals die al open waren (ook een Windows Terminal die al draait), houden hun oude PATH. Start ze opnieuw. Voor een sessie: herstart die sessie.
- **Herstellen** (PATH-ingang of kopie kwijt, bijvoorbeeld na een installer die je PATH herschreef): `pwsh -File "$HOME\.ao\daily\scripts\start-daily-ao.ps1" -InstallCli`. Dat start, stopt en bouwt niets.
- **Terugdraaien**: de rollback (hieronder) haalt de ingang weg en verwijdert de map. Je gebruikers-PATH komt dan **precies** terug zoals hij was, tenzij hij sindsdien door iets anders is aangepast: dan verwijdert het alleen de daily-ingang en laat het de rest staan. De oorspronkelijke waarde staat ook in `~/.ao/daily/backup/user-path-before-daily-cli.json`. Draait er nog een `ao.exe` uit de map, dan blijft de map staan tot dat proces stopt (niets vindt hem dan nog via de PATH).

## Bereikbaar blijven vanaf je telefoon

- **Laat de pc aan.** Een uitgezette pc is niet te bereiken. Controleer ook je energieplan: staat de pc op stroom niet in slaap- of sluimerstand, dan blijft hij bereikbaar.
- **Na een herstart** werkt je telefoon na ongeveer een minuut weer, ook zonder aanmelden, als er een opstart-taak was die door de overstap is omgezet. Agents die je dan start, draaien zonder venster. Na het aanmelden zie je ze gewoon in Ayo (daily).
- **Zet BitLocker aan als zo'n opstart-taak een opgeslagen wachtwoord gebruikt.** Windows bewaart dat wachtwoord als LSA-geheim op de schijf. Met BitLocker uit kan iemand die je schijf in handen krijgt dat in principe uitlezen.
  - Hoe: Start → typ **BitLocker beheren** → **BitLocker inschakelen** voor C:.
  - Bewaar de herstelsleutel in je Microsoft-account (of volgens het beleid van je werkgever).

## Terug naar de geïnstalleerde app

1. Run: `pwsh -File "$HOME\.ao\daily\scripts\rollback-to-installed-ao.ps1"`
2. Lees de melding over databasewijzigingen en antwoord `j`.
3. Alleen als de overstap een opstart-taak had omgezet: Windows vraagt weer één keer toestemming (UAC) en misschien je wachtwoord, net als bij het overstappen. Daarmee wordt die taak precies teruggezet zoals hij was; het script controleert dat. Klik je Nee, dan verandert er niets.
4. Het script stopt Ayo (daily) netjes, haalt de autostart weg, zet de oude snelkoppelingen terug en haalt de daily-`ao` van je gebruikers-PATH (de map `~/.ao/daily/bin` wordt verwijderd). Daarna start het Orchestrator.inc. Je sessies lopen door (ongeveer 30 seconden). De checkout, de losse tools en het opgeslagen GitHub-token blijven staan.

Opnieuw overstappen kan altijd met stap 2 van "Overstappen".

## Goed om te weten

- Ayo afsluiten laat de daemon en je agents doorlopen. Volgende keer starten pakt alles weer op.
- Gebruik `ao start` niet meer; dat start niet de daily-versie. Gebruik de snelkoppeling.
- De geïnstalleerde app werkt zichzelf niet bij zolang hij niet draait. Hij kan niets overschrijven, en als hij per ongeluk opent terwijl Ayo draait, sluit hij meteen weer.
- Na een backend-update kan de Firewall-vraag terugkomen (nieuw `ao.exe`-pad). Dat raakt alleen je telefoon via wifi; de tunnel blijft werken.
- In Taakplanner staat nu ook **Ayo (daily) openen**. Daarmee opent een update het venster op je bureaublad, ook als die update komt van een agent die al vóór het aanmelden liep. De rollback haalt die taak weg.
- Wijzig je het wachtwoord van je account, dan start een omgezette opstart-taak pas weer na een update van het opgeslagen wachtwoord. Doe dat in Taakplanner: de taak → Eigenschappen → OK → nieuw wachtwoord. Dat was voor de overstap ook al zo.
- De aanmeldlink `ao-app://` (terugkeer uit de browser na aanmelden bij Cloud) wordt bewaakt zolang de daily setup aan staat: elke start, elke update met herstart en de update-watcher zetten hem terug als hij ontbreekt of ergens anders heen wijst (alleen `HKCU`, geen admin). `-SwitchFromInstalled -CheckOnly` toont `sign-in link: OK / missing / points to …`. Een link uit de browser gaat naar het draaiende Ayo-venster (regel `[deep-link]` in `daily\logs\forge-*.log`); staat Ayo uit, dan gebeurt er niets.
- Autostart bij aanmelden uitzetten zonder terug te gaan: Taakbeheer → Opstart-apps → "Ayo (daily) autostart" uitschakelen.
- Getest in een simulatie (andere map, andere poort, eigen opslag voor het token): `scripts/daily-ao/tests`. Zie het bestand `tests/README.md` voor de opdrachten.
