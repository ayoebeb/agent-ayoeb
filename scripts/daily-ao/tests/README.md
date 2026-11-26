# Simulaties voor de daily-AO scripts

Alles draait in een eigen map onder `%TEMP%` (`ao-sim-*`), met een eigen "AO-map" (nooit een map die `.ao` heet: de named pipes zouden dan botsen met de echte AO), hoge poorten, en voor het GitHub-token Git Credential Manager's bestandsopslag in die map in plaats van Windows-referentiebeheer. Niets raakt de echte daemon (3001/3011), de echte `~/.ao`, de echte gebruikers-PATH of de echte Run-sleutel.

| Bestand | Wat het bewijst | Duur |
| --- | --- | --- |
| `sim-unit.ps1` | checksum-weigering, Node-volgorde (fnm, portable, PATH), Git-ontbreekt-melding, scripts kopiëren en bijwerken (ook: kapot script wordt niet overgenomen), geen pc-specifieke namen of paden, de aanmeldlink `ao-app://` op een eigen registersleutel (ontbreekt, fout, goed) | seconden |
| `sim-github-token.ps1` | het token per repository-URL tegen een nep-GitHub (`fake-github.mjs`): verkeerd account, te veel rechten en klassiek token geweigerd, clone en fetch terwijl het verkeerde `gh`-account actief is, verlopen token, geen lekken, en de bestaande git/gh-aanmeldingen van de pc (globale config en andere Credential Manager-items) blijven byte voor byte gelijk | ~1 minuut |
| `sim-guide-clone.ps1` | stap 2 van `tweede-pc-handleiding.nl.md` letterlijk, in Windows PowerShell 5.1 en PowerShell 7 | ~1 minuut |
| `sim-fresh-machine.ps1` | een verse pc zonder Node en Go: `-Prepare` installeert ze losjes (checksum), bouwt, en verandert niets buiten de simulatiemap | 15 tot 30 minuten (Defender maakt het uitpakken traag), ~4 GB, downloadt echt |
| `sim-bootstrap-pwsh.ps1` | `bootstrap-pwsh.ps1` in Windows PowerShell 5.1: losse PowerShell 7 met checksum | ~1 minuut, downloadt echt |

Draai ze vanaf de repository-root:

```powershell
pwsh -NoProfile -File scripts\daily-ao\tests\sim-unit.ps1
pwsh -NoProfile -File scripts\daily-ao\tests\sim-github-token.ps1
pwsh -NoProfile -File scripts\daily-ao\tests\sim-guide-clone.ps1
pwsh -NoProfile -File scripts\daily-ao\tests\sim-fresh-machine.ps1
pwsh -NoProfile -File scripts\daily-ao\tests\sim-bootstrap-pwsh.ps1
```

Elk script eindigt met `RESULT: <n> passed, <m> failed` en exitcode 0 bij geen fouten. Ze zijn niet aan CI gekoppeld (Windows, netwerk, lange duur). `-Keep` laat de simulatiemap staan voor onderzoek.

Wat een simulatie niet kan bewijzen: een echt fine-grained token op github.com (het nep-GitHub gedraagt zich als een privé-repository: 401 zonder inlog, 404 voor een ander account, 403 bij schrijven met een alleen-lezen token), een echte UAC-vraag, en de overstap zelf op een pc met een echte geïnstalleerde app.
