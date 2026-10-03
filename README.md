# agentiscode — Go

Samostatný CLI wrapper pro **OpenCode**, **Claude Code** a **claude-p**,
přepsaný z aplikace `agentiscode-cli` do Go. Spouští runtime, sjednocuje jeho
streamované události, zapisuje workflow výstupy a volitelně odesílá telemetrii
do Agentisu. Výsledkem sestavení je jedna binárka `agentiscode`.

## Sestavení a instalace

Vyžaduje **Go 1.22+** pro sestavení. Aplikace používá pouze standardní knihovnu;
pro její běh není potřeba Python, Poetry, pipx ani Go. Vybraný runtime
(`opencode`, `claude` nebo `claude-p`) musí být dostupný v `PATH`.

```bash
make build
./bin/agentiscode --help
./bin/agentiscode --version
```

Binárka je v `bin/agentiscode`. Build používá `CGO_ENABLED=0`. Volitelná
instalace do `/usr/local/bin`:

```bash
make install
```

Nebo instalace pro aktuálního uživatele:

```bash
make install PREFIX="$HOME/.local"
# Přidej $HOME/.local/bin do PATH, pokud tam ještě není.
```

Z Go projektu lze použít také `go install ./cmd/agentiscode`; cílem je potom
`GOBIN`, případně `$GOPATH/bin`. Plná správa procesních skupin je podporována
na Unixu (Linux/macOS). Na ostatních systémech existuje fallback ukončující
samotný proces; integrační testy procesních skupin jsou linuxové.

## Vydání nové verze

Po commitnutí změn (včetně `.github/workflows/release.yml`) vytvoř a pushni
anotovaný tag aktuálního commitu:

```bash
make release VERSION=1.2.3
# Lze zadat i VERSION=v1.2.3 nebo jiný remote: REMOTE=upstream
```

Target vyžaduje čistý pracovní strom a Git remote `origin` (lze změnit přes
`REMOTE`). Pushuje pouze vytvořený tag `v1.2.3`. Pokud push selže, tag zůstane
lokálně; po vyřešení chyby stačí `git push origin refs/tags/v1.2.3`.

GitHub Actions po pushnutí tagu `vX.Y.Z` spustí vet a testy na Linuxu, Windows
a macOS. Poté sestaví binárky pro **amd64 i arm64** a zveřejní GitHub Release
s automatickými release notes a souborem `SHA256SUMS`. Přílohy mají názvy:

- `agentiscode_v1.2.3_linux_amd64`, `agentiscode_v1.2.3_linux_arm64`
- `agentiscode_v1.2.3_windows_amd64.exe`, `agentiscode_v1.2.3_windows_arm64.exe`
- `agentiscode_v1.2.3_darwin_amd64`, `agentiscode_v1.2.3_darwin_arm64`

Verze se při buildu přebírá z tagu a je dostupná přes `--version`.
Tagy s příponou, například `make release VERSION=1.2.3-rc.1`, vytvářejí
prerelease. Publikování používá automatický `GITHUB_TOKEN` s oprávněním
`contents: write`; další secret není potřeba. Opakované spuštění workflow
aktualizuje přílohy existujícího releasu.

Staženou binárku na Linuxu/macOS označ jako spustitelnou (`chmod +x`) a případně
přejmenuj na `agentiscode`. Na Windows ji lze přejmenovat na `agentiscode.exe`.

## Použití

```bash
./bin/agentiscode --adapter opencode --model openai/gpt-5 "fix the failing test"
./bin/agentiscode --adapter claude --model claude-sonnet-4-5 "review the API"
printf '%s' 'continue the task' | ./bin/agentiscode --adapter claude-p --resume SESSION_ID --json
```

Bez `--json` jde text asistenta na stdout, aktivita a diagnostika na stderr.
S `--json` je stdout proud samostatných JSON objektů, jeden na řádku:

```json
{"type":"session","adapter":"opencode","session_id":"ses_1","provider":"opencode"}
{"type":"text","text":"Hotovo","session_id":"ses_1"}
{"type":"result","session_id":"ses_1","usage":null,"cost_usd":null,"is_error":false}
```

### Volby

| Volba | Význam |
| --- | --- |
| `-a`, `--adapter NAME` | Povinný runtime: `opencode`, `claude`, `claude-p`. |
| `-m`, `--model MODEL` | Model předaný runtime. |
| `-e`, `--effort VALUE` | Claude `--effort`, OpenCode `--variant`. |
| `--agent NAME` | Pojmenovaný agent nebo mode CLI. |
| `--cwd PATH` | Pracovní adresář, výchozí je aktuální. |
| `--resume SESSION_ID` | Pokračování existující session. |
| `--timeout SECONDS` | Limit procesu v sekundách, včetně zápisu promptu; `0` = bez limitu. Podporuje desetinná čísla. |
| `--json` | Normalizované JSON Lines na stdout. |
| `--task-id TASK_ID` | Telemetrie tasku, založení runu, pokud chybí `--run-id`. |
| `--project-id PROJECT_ID` | Kontext projektu, default `AGENTIS_PROJECT_ID`. |
| `--run-id RUN_ID` | Existující run, vyžaduje `--task-id`. |
| `--task-status STATUS_ID` | Stav tasku předaný s volitelným finálním komentářem. |
| `--last-message-to-comment` | Odeslat poslední textový blok jako primary komentář. |
| `--primary-session BOOL` | Označit první session jako primární, default `true`. |
| `--agentis-api URL` | Agentis JSON-RPC endpoint, default `AGENTIS_ENDPOINT`. |
| `--agentis-token TOKEN` | User token: `AGENTIS_API_TOKEN`, fallback `AGENTIS_TOKEN`. |
| `--agentis-service-token TOKEN` | Service token: `AGENTIS_SERVICE_TOKEN`. |
| `--final-output PATH` | Zapsat poslední souvislý blok textu po dokončení běhu. |
| `--session-output PATH` | Zapsat první session ID ihned po jeho získání. |
| `-h`, `--help` | Nápověda. |
| `--version` | Verze aplikace. |
| `PROMPT ...` | Slova promptu; bez argumentů se čte stdin. |

Aliasy: `oc` → `opencode`; `cloud`, `cc`, `claudecode`, `claude-code` →
`claude`; `cp`, `claudep` → `claude-p`. Názvy adaptérů nerozlišují velikost
písmen. Boolean hodnoty podporují `true/false`, `1/0`, `yes/no`, `y/n`, `on/off`.
Volby lze zapisovat jako `--model=value` i `--model value`, krátké také `-mvalue`.
`--` ukončí parsování voleb. Používej celé názvy dlouhých voleb nebo uvedené
krátké aliasy; automatické zkratky názvů z Python argparse nejsou podporovány.

Prompt se předává runtime přes stdin, nikoli jako argument příkazu. Pro novou
session jsou připojeny XML tagy `agentis_task_id` a `agentis_project_id`, pokud
jsou zadány. Při `--resume` se tagy nepřidávají. Runtime dostává `IS_SANDBOX=1`
a stejné výchozí permission flags jako Python verze. Claude navíc dostává
`--disallowedTools AskUserQuestion`; `claude-p` nemá prefix `--print -`.

### Agentis a workflow

```bash
./bin/agentiscode --adapter opencode \
  --task-id "$AGENTIS_TASK_ID" \
  --run-id "$AGENTIS_RUN_ID" \
  --agentis-api "$AGENTIS_ENDPOINT" \
  --final-output .agentis/outputs/final-comment.md \
  --session-output .agentis/outputs/session-id \
  < "$AGENTIS_PROMPT_FILE"
```

Telemetrie vytváří run přes `task.start_run` s `start_adapter=false`, navazuje
sessions pomocí `run.store_session_id` a posílá OpenCode-kompatibilní transcript
do `session.store_activity_log`. Podřízené sessions mají vlastní transcript a
nejsou primární. Vazba session musí uspět před odesláním její aktivity; neúspěšná
vazba či snapshot se opakuje při dalším eventu nebo dokončení.

Vlastní run se dokončuje přes `run.adapter_event` s `kind=idle`. **Externí
`--run-id` se neukončuje** — dokončení patří workflow orchestrátoru.
Komentář vzniká pouze s `--last-message-to-comment`. Text pro komentář a finální
soubor je poslední souvislý blok `text` eventů; `reasoning`, `tool` a `step`
vytvářejí hranici bloku. Soubor končí novým řádkem, rodičovské adresáře se vytvoří.

RPC rozlišuje user hlavičku `X-Auth-Token` a service hlavičku `X-Service-Token`
podle metody. Tokeny z CLI jsou v diagnostice příkazu redigované. HTTP přesměrování
se nenásledují. RPC má samostatný desetisekundový limit na požadavek; jeho chyby
jsou best-effort a nemění úspěšný výsledek samotného agenta.

### Události a návratové kódy

| Event | Obsah |
| --- | --- |
| `session` | Adaptér, session ID a dostupná metadata. |
| `text` | Append-only delta odpovědi. |
| `reasoning` | Append-only delta reasoning/thinking. |
| `tool` | ID volání, název, stav, vstup a dostupný výstup/chyba. |
| `step` | Spotřeba a cena jednoho turnu. |
| `result` | Finální souhrn, `is_error`; OpenCode jej syntetizuje. |
| `error` | Chyba běhu. |
| `stderr` | Diagnostický řádek runtime. |

OpenCode snapshoty se převádějí na delty podle part ID. Opakované running
snapshoty nástroje se deduplikují. Claude `message_id` zůstává na dílčích
událostech a usage se započítá jednou na zprávu. Finální usage se v telemetrii
nezapočítává znovu, pokud už byly přijaty per-turn `step` události. Čtečka
podporuje řádky větší než 64 KiB i poslední řádek bez nového řádku.

Návratové kódy: `0` úspěch, `1` chyba runtime, `2` chybné CLI argumenty,
`130` SIGINT, `143` SIGTERM. Při signálu se ukončí procesní skupina runtime;
finální soubor a dokončovací telemetrie se nezapisují. Claude dostává po
terminálním `result` maximálně deset sekund na ukončení procesu.

## Go API a struktura

Kořenový balíček `agentiscode` lze použít i jako knihovnu:

```go
wrapper, err := agentiscode.NewWrapper(agentiscode.Config{
    Adapter: "opencode",
    Model:   "openai/gpt-5",
    Cwd:     "/work/project",
    Timeout: 10 * time.Minute,
})
if err != nil { return err }
return wrapper.Stream(ctx, "Oprav testy", func(event agentiscode.Event) error {
    return json.NewEncoder(os.Stdout).Encode(event)
})
```

`StreamNative` zpřístupňuje native eventy; `Collect` vrací souhrn a události;
`StreamSSE` zapisuje **sjednocené** události jako SSE a flushuje podporované
writery. V `Config` jsou navíc `Command`, `ExtraArgs`, `Env`, `ExitGrace` a
Claude volby `Chrome`, `MaxTurns`, `AppendSystemPromptFile`, `AddDirs`.
`RequirePermissions` může v knihovním API vypnout výchozí skip-permissions.
Každá instance wrapperu, normalizátoru, mapperu a telemetrie patří jednomu běhu;
její metody volej sériově. Chyba callbacku zastaví stream a ukončí runtime.

| Soubor | Zodpovědnost |
| --- | --- |
| `cmd/agentiscode/main.go` | Vstupní bod a signály. |
| `cli.go`, `output.go` | Parsování voleb, renderování, workflow soubory. |
| `config.go`, `event.go` | Konfigurace, události, pomocné konverze. |
| `runner.go`, `process_*.go` | Procesy, stdin/stdout/stderr, timeouty, SSE. |
| `normalize.go` | Native protokoly a sjednocující překladače. |
| `mapper.go` | OpenCode-kompatibilní transcript. |
| `rpc.go`, `telemetry.go` | JSON-RPC klient a lifecycle telemetrie. |

### Opravy oproti Python verzi

- Timeout funguje i při zablokovaném zápisu promptu a ukončuje procesní skupinu.
- Chyba startu nebo nenulový exit se promítne do syntetického `result.is_error`.
- Selhání před první session označí vlastní Agentis run jako neúspěšný.
- Chybové eventy telemetrie nepřidávají ne-JSON diagnostiku na stdout.
- Claude tool chyba se zachová i při předání v poli `output`.
- Stderr se doručuje průběžně i v době, kdy stdout nic neposílá.
- Runtime se spouští přímo, bez závislosti na Bash mezivrstvě.

## Testování

```bash
make check
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
GOMAXPROCS=2 go test -run '^$' -fuzz FuzzNormalization -fuzztime 8s -parallel 2
```

Testy používají lokální falešné runtime procesy a HTTP servery. Pokrývají
CLI, normalizaci, metadata a tokeny, telemetrii, dlouhé prompty/řádky,
timeouty, process groups a signály. Nevolají živé agenty ani produkční Agentis.

Volitelné migrační porovnání s původním Python projektem:

```bash
AGENTISCODE_PYTHON_SOURCE=/var/www/agentiscode-cli \
  go test -race -run TestPythonCompatibility -timeout 120s ./...
```

Tento test vyžaduje funkční `.venv/bin/python` původního projektu. Ve 12
scénářích porovnává normalizované JSON eventy, workflow soubory i RPC payloady
(včetně autentizačních hlaviček); ignoruje pouze náhodně generovaná ID a časové
značky transcriptu. Běžné sestavení a testy Python nepotřebují.
