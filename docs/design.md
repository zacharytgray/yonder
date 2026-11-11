# Yonder design

Yonder connects a person's own machines (one to five, macOS and Linux) so the AI agents on any of
them can see and use all of them. One Go binary, `yonder`, installed on every machine. No daemon,
no server, no account. Tailscale carries the network; plain OpenSSH carries the commands.

## Principles

- Plain SSH over the tailnet. Yonder adds a registry, discovery, secrets, a log and a skill.
- The model never sees a secret value. It only ever handles secret names.
- Permissions belong to the harness. Yonder connects machines and records what crossed them.
- Every interactive prompt has a flag, so an agent can run onboarding too.
- Fail loud. `doctor` explains every broken or inert piece and prints the fix.

## Layout

```
cmd/yonder/            main
internal/cli/          one file per command (cobra)
internal/registry/     machine records, tombstones, local config, merge
internal/tailnet/      tailscale status --json
internal/transport/    ssh runner (system ssh binary)
internal/remote/       the hidden _exec / _facts / _sync / _log commands run on the target
internal/runlog/       JSONL command log
internal/discover/     hardware, models, harnesses
internal/secrets/      providers (1password, bws) and output scrubbing
internal/agents/       embedded SKILL.md and per-harness install
internal/doctor/       checks
internal/version/      version string, set with -ldflags
skill/yonder/SKILL.md  the agent skill (embedded into the binary)
test/                  end-to-end tests against the built binary (+ docker integration)
```

The hidden commands a caller runs on a target are `_exec`, `_job` (the detached supervisor),
`_jobs`, `_sync`, `_log`, `_facts` and `_init-self <name>`.

## Files on each machine

```
~/.config/yonder/config.toml           this machine: self = "<name>", secrets provider settings
~/.config/yonder/machines/<name>.toml  one record per fleet machine (a copy on every machine)
~/.config/yonder/removed/<name>.toml   tombstones, so refresh never re-adds a removed machine
~/.config/yonder/secrets.toml          NAME = "op://vault/item/field" references, never values
~/.local/state/yonder/log.jsonl        commands this machine received
~/.local/state/yonder/jobs/<id>.log    output of detached runs (scrubbed)
~/.local/state/yonder/jobs/<id>.json   detached run status: pid, state, exit
~/.local/state/yonder/lost-keys-applied  lost tombstones already stripped here: "<name> <removed_at>"
```

`XDG_CONFIG_HOME` and `XDG_STATE_HOME` are honoured. `YONDER_HOME` overrides both (tests):
config goes in `$YONDER_HOME/config`, state in `$YONDER_HOME/state`, and the files yonder touches
under `~` (harness skill folders, `~/.ssh/authorized_keys`) go under `$YONDER_HOME/home`.

## Machine record

```toml
name = "gpu-box"              # tailscale short hostname, kebab-case
os = "linux"                  # linux | darwin
arch = "amd64"
tailscale_ip = "100.x.y.z"
dns_name = "gpu-box.example.ts.net"
ssh_user = "me"
ssh_port = 22
yonder_path = "/home/me/.local/bin/yonder"
ssh_host_key = "ssh-ed25519 AAAA..."   # this machine's sshd host key, pinned at add time
ssh_client_keys = ["ssh-ed25519 AAAA... me@gpu-box"]  # keys this machine uses to reach others
role = "gpu"                  # optional, user-given: laptop | desktop | server | gpu | other
notes = "sleeps at night, heavy jobs only"
updated_at = 2026-11-11T20:00:00Z
discovered_at = 2026-11-11T20:00:00Z
added_at = 2026-11-11T20:00:00Z  # last `yonder add` (or first `init` here); only this beats a tombstone

pending = true                # only while setup is unfinished (peer was offline at add time)

[facts]                       # written by discovery (`_facts`), owned by the machine itself
hostname = "gpu-box"
os = "linux"
arch = "amd64"
os_version = "Ubuntu 24.04.1 LTS"   # macOS: "macOS 15.1"
laptop = false                # a battery was found
cpu = "AMD Ryzen 9 7950X 16-Core Processor"
cpu_cores = 16
memory_gb = 64.0              # GiB, one decimal (disk and gpu memory too)
disk_total_gb = 1800.0        # root filesystem
disk_free_gb = 512.0
[[facts.gpus]]                # name, vendor, memory_gb, memory_used_gb, util_percent, unified
[[facts.models]]              # name, runtime, size_gb, source, serving, port
[[facts.harnesses]]           # name (claude-code, codex, pi, hermes, opencode, gemini), version, path
[facts.extra]                 # anything without a typed field yet
```

A tombstone is `removed/<name>.toml` with `name`, `removed_at`, an optional `reason`, the
machine's `ssh_client_keys`, and `lost = true` after `remove --lost`. It also keeps `held_keys`:
the `ssh_client_keys` of the record this machine held for that name when the tombstone replaced
it. `held_keys` stays local and is never synced.

Records merge by `updated_at`, newest wins, per file; the merged record keeps the latest
`added_at` of the two. A tombstone drops the record unless the record's `added_at` is at or after
`removed_at` (the machine was re-added), in which case the tombstone is dropped. `updated_at` does
not count here: a removed machine that comes back and refreshes its own record gets a newer
`updated_at` but keeps its old `added_at`, so the tombstone still wins and it can't resurrect
itself. On equal `removed_at` the lost tombstone wins. A lost tombstone always drops the record,
whatever its `added_at`; only a local `yonder add` clears it (see Trust model).

`config.toml` holds `self = "<name>"` and a `[secrets]` table for provider settings. Yonder
rewrites only `self` and `[secrets]` and keeps every other key.

## Reaching a machine

1. Look up the record. If it is this machine, run locally with no SSH.
2. Ask `tailscale status --json`. If the peer is `Online: false`, stop: exit 75,
   `yonder: gpu-box is offline (last seen 2h ago) — notes: sleeps at night`.
3. The record's `tailscale_ip` must be the peer's current tailnet address. If not, stop: exit 1,
   `yonder: gpu-box's address changed (record X, tailnet Y); run 'yonder doctor'`. Yonder never
   moves a peer's record to a new address on its own; `yonder remove <m> && yonder add <m>` does.
4. `ssh -T -o BatchMode=yes -o ConnectTimeout=5 -o Port=<port> -o User=<user> <tailscale_ip>
   <yonder_path> _exec`, with the host key pinned (below). SSH exit 255 means unreachable:
   exit 75, `yonder: gpu-box is unreachable over ssh (<ssh's error>) — notes: ...`.

A remote command that itself exits 255 is indistinguishable from an ssh failure and is
reported as unreachable. Commands over ssh start in the target user's home; local runs start
in the current directory.

Host key pinning: every ssh, scp and ssh-copy-id call gets a fresh temp `known_hosts` holding
only `yonder-<name> <ssh_host_key>`, plus `HostKeyAlias=yonder-<name>`,
`GlobalKnownHostsFile=/dev/null` and `StrictHostKeyChecking=yes`. The user's own `known_hosts`
is never read or written. `yonder add` is the one trust moment: it connects with
`StrictHostKeyChecking=accept-new` against an empty temp file and reads the key back. A
machine's own record takes its key from `/etc/ssh/ssh_host_*_key.pub`.

`YONDER_SSH_IDENTITY=<private key>` adds `-i <key> -o IdentitiesOnly=yes` (tests and CI).

Tailscale cannot tell a sleeping machine from a switched-off one, so Yonder says "offline" and
shows last seen plus the user's notes. It does not guess.

`YONDER_TAILSCALE_JSON=<file>` replaces the live status with a fixture (tests and CI).

## The `_exec` envelope

The caller writes one JSON line to the remote `yonder _exec` on stdin, then passes its own stdin
through. The envelope holds the command, the caller's machine and harness, the secret names and
their values, and whether to detach. Values never appear in argv, so they never show up in `ps`
or shell history. The target:

1. runs the command with `$SHELL -lc '<cmd>'`, secrets added to its environment
2. scrubs every secret value out of stdout and stderr (replaced with `[NAME]`; overlapping
   values become one `[A+B]`)
3. appends one line to its log: time, id, from, to, harness, user, command (secret values
   scrubbed), secret names, exit, ms
4. exits with the command's exit code

Detached runs use `setsid`/`nohup`, write output to `jobs/<id>.log`, and return the id at once.
Concretely, `_exec` re-runs its own binary as `yonder _job` in a new session (`Setsid`, SIGHUP
ignored), hands it the envelope on a pipe, logs the launch with `"exit": null`, prints the id,
and exits 0. `_job` runs the command with output scrubbed into the log file and writes
`jobs/<id>.json`. A job whose pid has gone while still marked running shows as `lost`.

Envelope fields, one JSON object on one line:

```json
{"v":1,"id":"20261111-200000-ab12cd","command":"nvidia-smi -L","from":"laptop",
 "to":"gpu-box","harness":"claude-code","secrets":{"API_KEY":"<value>"},"detach":false}
```

`v` is the envelope version, at least 1; a target refuses a missing or lower one, and refuses a
newer one and says to upgrade. `yonder run m --
cmd` with one argument sends it as the shell string as-is; with several, each is single-quoted
and joined (a word with only safe characters stays bare, except one starting with `=`, which zsh
would expand as `=cmd`; `FOO=bar` stays bare so env assignments work). `harness` comes from `--as`, else the environment: `CLAUDECODE=1` (claude-code),
`CODEX_THREAD_ID` or `CODEX_SANDBOX` (codex), `PI_CODING_AGENT=true` (pi), `HERMES_SESSION_ID`
(hermes), `AI_AGENT` (its value), else `shell`.

Log lines (`log.jsonl`, one JSON object each):

```json
{"time":"...","id":"...","from":"laptop","to":"gpu-box","harness":"codex","user":"me",
 "command":"...","secrets":["API_KEY"],"exit":0,"ms":42,"side":"target"}
```

`side` is `target` on the machine that ran the command. When the caller cannot reach the
target (offline, unreachable) it logs the attempt locally with `"side":"caller"` and
`"exit":75`. Detached launches add `"detached":true` with `"exit":null`. Failures add `error`.
Secret values never appear in the log. `yonder log` merges every online machine's `_log` with
the local file, dedupes and sorts by time.

## Adding machines

`yonder add <m>` requires `m` to be a tailnet peer (not iOS/Android). It probes over ssh with
BatchMode. If key auth fails and a terminal is attached, it offers `ssh-copy-id`; otherwise it
prints the `ssh-copy-id` line and stops. It looks for yonder via `sh -lc 'command -v yonder'` (dir resolved with `pwd -P`, path cleaned),
then `~/.local/bin`, `/opt/homebrew/bin`, `/usr/local/bin`, `/home/linuxbrew/.linuxbrew/bin`
and `~/go/bin`. If yonder is missing and the target's os/arch match this binary, it copies
itself to `~/.local/bin/yonder` with scp. Otherwise it runs the official install script there
(`curl -fsSL https://raw.githubusercontent.com/zacharytgray/yonder/main/install.sh | sh`, or
`wget -qO-` when curl is missing) and checks for `~/.local/bin/yonder` afterwards, since
`curl | sh` exits 0 even when the download fails. If that fails too (no release published, no
network there), it prints the manual install lines and stops. It reads
every `~/.ssh/*.pub` on the target into `ssh_client_keys` (all of them, not only the key that
machine uses for yonder; sync never strips a key a live record lists, see Trust model), runs `_facts`, saves the record, runs
`_init-self <m>` there, and syncs the registry to every online machine. An offline peer is
saved as `pending = true` and `add` exits 75; `yonder refresh` finishes it once it is online.
A set-up record with no `ssh_host_key` is an error with fix `yonder add <m>`; `refresh` never
pins a new key for one.

`yonder init --yes` sets up only this machine unless `--machines a,b` names peers. After adding
peers, init also installs the skill on each machine it added that is online and set up (the
`agents install --all-machines` path), asking once "Install the skill on <names> too?" unless
`--yes`, and prints one line per machine. Failures there are warnings.

Sync: the caller sends its bundle (`{"machines":[...],"tombstones":[...],"clear_lost":[...]}`) to
each online machine's `_sync`, merges each reply as coming from that machine (same rules as
`_sync`, see Trust model), saves, then resends to any machine whose reply was missing something.

Lost-key cleanup rides on sync. After merging, `_sync` looks at every `lost = true` tombstone
it now holds, received or already there, and strips that machine's keys from its own
`~/.ssh/authorized_keys` with the same code as `remove --strip-keys` (same match rules, same
backup). It strips only keys in both the tombstone and its `held_keys`, and never a key that any
live record (self included) lists. The caller does the same on itself after every sync (`refresh`,
`add`, `remove`), even with no peer online. Each tombstone is applied once per machine and
recorded in `lost-keys-applied`, so later syncs don't touch the file, and a key the user adds
back by hand stays. A failed strip is not recorded, so the next sync retries it. It never
fails the sync; `doctor` reports a key left behind.

## `ls --json` and `show --json`

Agents parse these. Keys are always present; new keys may be added, none removed or renamed.

```json
{
  "self": "laptop",
  "tailscale_error": "",
  "machines": [
    {
      "name": "gpu-box", "self": false,
      "state": "offline",
      "online": false,
      "last_seen": "2026-11-11T18:00:00Z",
      "role": "gpu", "notes": "sleeps at night",
      "os": "linux", "arch": "amd64",
      "tailscale_ip": "100.64.0.2", "dns_name": "gpu-box.example.ts.net",
      "ssh_user": "me", "ssh_port": 22,
      "hardware": "16 cores, 64 GB RAM, Example GPU",
      "summary": "Example GPU 24 GB · 64 GB",
      "model_count": 3, "harnesses": ["claude-code", "codex"],
      "stale": false,
      "discovered_at": "...", "updated_at": "...",
      "facts": { "...": "as in the record, arrays never null" }
    }
  ]
}
```

- `hardware`: long one-line form. `summary`: the short form `ls` prints (`M3 Pro · 36 GB`,
  `RTX 4090 24 GB · 64 GB`, or a trimmed CPU name when there is no real GPU).
- `state`: `self`, `online`, `offline`, `pending` (setup unfinished), `missing` (not in
  tailscale status) or `unknown` (tailscale unavailable; see `tailscale_error`).
- `online`: `null` when tailscale is unavailable.
- `last_seen`: set only for offline peers that tailscale has seen; else `null`.
- `stale`: `discovered_at` is older than 7 days (or never, for a set-up machine).

Lazy refresh: before printing, `ls`, `ls --json` and `show` rediscover any machine whose
`discovered_at` is older than 24 hours (or never set): this machine locally, and each online,
set-up peer with `_facts`, all in parallel, through the same code `refresh` uses. They wait at
most 5 seconds in total. A machine that answers in time gets its record updated; one that is
slow or fails keeps what is stored, with no error. Results are saved on this machine only, not
synced, so `ls` costs one `_facts` per stale peer and nothing more; the next `refresh` or `add`
spreads them. Pending and offline machines are skipped. `--no-refresh` prints the stored
facts as they are. The 7-day `stale` marker still applies to whatever is printed.

Discovery (`internal/discover`) runs on the machine itself, all probes concurrent, each with its own
timeout (about 3 s total). A missing tool is absent from the facts, never an error.

- Hardware: `sysctl`, `sw_vers`, `pmset -g batt`, `system_profiler SPDisplaysDataType` (Intel
  Macs only; Apple silicon reports the chip as one unified-memory GPU), `/proc`, `/etc/os-release`,
  `/sys/class/power_supply/BAT*`, `nvidia-smi`, `rocm-smi`.
- Models: Ollama (`:11434/api/tags`, else `ollama list`), LM Studio (`:1234/v1/models` and
  `lms ls --json`), OpenAI-compatible servers on `:8080` and `:8000` (llama.cpp, vLLM, MLX).
  `source` is `api` or `cli`. `serving` is true when a local server listed it, with its `port`.
  Runtime is guessed from `owned_by` for the unnamed servers (`llamacpp`, `vllm`, `mlx`,
  else `openai-api`).
- Harnesses: each binary is searched on `PATH` plus `~/.local/bin`, `/opt/homebrew/bin`,
  `/usr/local/bin`, `~/.npm-global/bin` and `~/.bun/bin`; version is the first version number in
  `<bin> --version` (2 s timeout).

`show <m> --json` prints one machine object with one extra key, `record`, holding the full
record (including `yonder_path`, `ssh_host_key`, `ssh_client_keys`).

## Exit codes

- the remote command's own code, passed through
- 75: target offline or unreachable (EX_TEMPFAIL); agents should tell the user, not retry
- 2: usage error
- 1: any other yonder error

## Secrets

`secrets.toml` maps names to references. The machine the agent runs on resolves the values with its
configured provider, then sends them inside the envelope over SSH. Values live only in that yonder
process's memory, the envelope on ssh stdin and the remote command's environment. Never in argv,
logs, errors or output. The remote command's environment can be read by other processes of the
same user on that machine (for example through `/proc/<pid>/environ` on Linux).

```toml
# ~/.config/yonder/secrets.toml (0600)
API_KEY = "op://example-vault/api/credential"     # op://vault/item/[section/]field
DB_URL  = "bws://11111111-2222-3333-4444-555555555555"   # bws://<secret uuid>
```

Names are env var names, `[A-Z_][A-Z0-9_]*`. `secrets.toml` is per machine and never synced:
providers and tokens differ per machine, and a machine only needs the names its agents use. Add the
same names on each machine an agent runs from with `yonder secret add`.

```toml
# config.toml
[secrets]
provider = "1password"        # 1password | bws
mode = "desktop"              # 1password only: desktop | service-account
token_store = "file"          # optional: keychain | secret-service | file (default: picked per OS)
```

Providers:

- `1password` with `mode = "desktop"`: `op` with the desktop app integration (Touch ID, Apple Watch,
  system auth). The approval lasts until 10 minutes idle or 12 hours. `OP_SERVICE_ACCOUNT_TOKEN` is
  removed from `op`'s environment in this mode.
- `1password` with `mode = "service-account"`: hands-free. A read-only service account per machine,
  scoped to one dedicated vault (service accounts cannot see Personal, Private or the default Shared
  vault). Individual and Families plans allow 1,000 reads a day.
- `bws`: Bitwarden Secrets Manager with a machine-account access token.

1Password cannot approve CLI reads from its phone app today. When it can, that becomes a third mode.

`yonder run --secret A --secret B` makes one provider call for all names. 1Password gets one
`op inject` with a template on stdin: each reference sits between random boundary lines, so a value
may hold newlines and still parse back. bws gets one `bws secret get <uuid> --output json` per name.
Unknown names fail before any provider call and list the known names.

Tokens for hands-free modes go to the provider only through the child's env
(`OP_SERVICE_ACCOUNT_TOKEN`, `BWS_ACCESS_TOKEN`). If that variable is already set in yonder's own
environment it wins over the store. The store, by OS:

- macOS: the login Keychain, service `yonder`, account `1password` or `bws`. Writing runs
  `/usr/bin/security -i` and sends the `add-generic-password -U ... -w <token>` line on its stdin,
  so the token is never in any process's argv. Tokens are limited to `[A-Za-z0-9._:=+/~-]` so the
  line can't be split. Reading is `security find-generic-password -w`.
- Linux with `secret-tool` and a D-Bus session: `secret-tool store` reads the token from stdin.
- Elsewhere: `~/.config/yonder/tokens/<provider>`, mode 0600. `doctor` names the file and refuses a
  token file other users can read.

Commands: `yonder secret add NAME <ref>`, `ls [--json]` (names and references, never values),
`rm NAME`, `test NAME` (resolves, prints only `NAME: ok, N chars`), `setup [--provider p --mode m]`
and `token set` (hidden prompt, or stdin: `yonder secret token set < tokenfile`).
`secret ls --json` is `{"provider":"1password","mode":"desktop","secrets":[{"name":"...","ref":"..."}]}`.

`init` runs the same setup as `yonder secret setup`, but leaves an existing provider alone. It
recommends desktop mode where the 1Password app is installed and the session is not headless, and
service-account mode otherwise. With `--yes` it only prints the non-interactive commands.

`doctor` gets `secrets.Health()`: provider configured, CLI installed and its version, for desktop
whether `op` can see the app (`op whoami`, then `op account list`; no item is read), for hands-free
modes whether a token is stored (and for 1Password, whether `op whoami` accepts it), and whether each
reference is valid for this machine's provider. Every failure carries a fix.

## Agent skill

`yonder agents install` copies `skill/yonder/` (embedded with `go:embed` from `skill/embed.go`)
into each harness's user skills folder: `~/.claude/skills/` (Claude Code; it doesn't read
`~/.agents/skills`), `~/.agents/skills/` (Codex, Pi), `~/.hermes/skills/` (Hermes). It writes only
its own `yonder/` folder and never edits a harness's settings file.

- A harness counts as installed if its binary (`claude`, `codex`, `pi`, `hermes`) is on PATH or
  its config dir (`~/.claude`, `~/.codex`, `~/.pi`, `~/.hermes`) exists. `--harness h` (repeatable)
  installs for h anyway. `--dry-run` writes nothing.
- The folder is built in a temp dir beside the target and swapped in with renames. A symlink or
  file at the target is left alone. The folder holds `.yonder-version`: `<version> <skill hash>`.
- `agents status [--json]` shows each harness: `detected`, `path`, `state` (`current`, `outdated`,
  `missing`, `foreign`) and `installed_version`. JSON: `{"version","skill_hash","harnesses":[...]}`.
- `agents uninstall` removes the folder from every harness (or the `--harness` ones).
- `agents install --all-machines` also runs `yonder agents install` on each online machine
  through `_exec`, so each target logs it.
- `init` runs the install for detected harnesses and prints one line per harness.
- `doctor` checks a remote machine's skill only for harnesses in that machine's `facts.harnesses`
  (the remote's own detection also counts stray config dirs). Without facts it trusts the remote.

## Doctor

`yonder doctor [--json]` prints one line per check, grouped by machine: PASS, WARN or FAIL, a
one-line detail, and a fix under anything not passing. It exits 1 if any check failed, else 0.

- This machine: tailscale installed, running, logged in; config and self record (and whether a
  tombstone says it was removed); sshd host key; stale facts; log dir writable; the skill for each
  detected harness; `authorized_keys` free of lost machines' keys; `secrets.Health()`.
- Each fleet machine, in parallel: in tailscale status; online (offline is a WARN with last seen
  and notes, and its other checks are skipped); pending; no pinned host key (fix `yonder add
  <m>`); record address matches the tailnet (FAIL, fix `yonder remove <m> && yonder add <m>`); ssh with the pinned host key, by running
  `yonder version` there (BatchMode, 5s connect timeout); same yonder version as here; stale
  record (`discovered_at` older than 7 days, fix `yonder refresh <m>`); skill state from
  `yonder agents status --json`; its `authorized_keys` when there are lost machines.
- Registry consistency: each online machine's `_sync` reply to an empty bundle (it merges nothing
  and returns what that machine has) must name the same machines as this one.

`--json`: `{"self","version","ok","summary":{"pass","warn","fail"},"checks":[{"group","name",
"status","detail","fix"}]}`.

## Removing a machine

`yonder remove <m> [--lost] [--yes]` writes a tombstone, deletes the record here and pushes the
tombstone to every online peer with the usual sync. It asks first unless `--yes`, and with no
terminal it needs `--yes`. It refuses to remove `self`. The removal is logged here as a
caller-side entry (`"command":"yonder remove <m>[ --lost]"`).

- Default: if `m` is online, yonder first runs `yonder agents uninstall && yonder remove
  --forget-local` there through `_exec`. `--forget-local` clears that machine's `machines/`,
  `removed/` and `self`, and keeps the rest of `config.toml`. The binary stays; the output says
  how to delete it. If `m` is offline, the output gives that line to run there later. A pending
  `m` (never set up) only prints `removed <m> (it was never set up)`, plus the tombstone sync.
- `--lost`: `m` is never contacted. On this machine and every online peer (`yonder remove
  --strip-keys` through `_exec`, keys on stdin, before the sync) yonder removes the `authorized_keys` lines whose
  key type and base64 match one of `m`'s `ssh_client_keys`. Options prefixes and comments don't
  matter for the match, and other lines are kept byte for byte. The original is copied to
  `authorized_keys.yonder-bak.<unix-nanos>` first, a new backup per change, so a later strip
  never overwrites an earlier backup; a missing file is fine. Then it prints a checklist: delete
  `m` from the tailnet, revoke its secrets token, rotate what it could read, remove its keys from
  other services, and note which machines it couldn't reach. Those strip the keys themselves when
  a later sync reaches them (see Sync above). Running `remove --lost` again works from the
  tombstone alone and always strips, applied or not; `doctor` fails while any machine still
  trusts a lost key.
- Offline peers keep the record until a sync reaches them; any later `yonder refresh` from a
  machine that has the tombstone pushes it, and a lost tombstone strips the keys there too. A removed machine that comes back and syncs is refused
  by every machine holding its tombstone (see Trust model).
- The tombstone is dated now. If the record's `added_at` is in the future (clock skew), it is
  dated one second after it instead, but never more than 4 minutes ahead, so peers' skew guard
  still takes it. The record is dropped here either way.

## Trust model

Every fleet machine trusts every other fully. A machine that can ssh in as the user can run any
command there, edit any file, and set its own environment (`SSH_CONNECTION` included). No rule
inside `_sync` can stop a hostile machine, and these rules don't try to. They stop honest
mistakes from spreading: stale or wrong records, clock skew, and a removed machine adding
itself back on its next sync.

The defense against a lost or stolen machine is cutting its network access. `remove --lost`
lists removing it from the tailnet first, because that stops it reaching every machine at
once. Stripping its ssh keys is the second layer, for machines it could still reach another
way, such as the same LAN.

- `_sync` under sshd names its sender. It takes the source address from `SSH_CONNECTION`,
  finds that tailnet peer, then the record whose `tailscale_ip` is one of the peer's addresses.
  If the sender is not a tailnet peer, has no record here, or has a tombstone here, it refuses
  the whole sync and exits 1. A machine whose registry is empty (just added) takes its first
  sync from whoever got in over ssh. `_sync` with no `SSH_CONNECTION` comes from yonder itself
  on this machine and is trusted.
- A peer can't change this machine's own record or send a tombstone naming it. The one
  exception: a machine with no record of itself yet takes it from the first sync.
- Records and tombstones whose `updated_at`, `added_at` or `removed_at` is more than 5 minutes
  ahead of this machine's clock are dropped.
- For a record already held here (and set up), `tailscale_ip`, `ssh_host_key`,
  `ssh_client_keys` and `added_at` don't change through sync. Facts, role, notes,
  `yonder_path`, `discovered_at` and `updated_at` merge as usual. A pending record is finished
  by the first full one. To change a machine's identity, run `yonder remove <m>` then
  `yonder add <m>`.
- A plain tombstone loses to a record with a newer `added_at` (a re-add). A lost tombstone
  never loses through sync. `yonder add <m>` run here clears a local lost tombstone for `m`,
  then its sync sends `"clear_lost":["m"]`; a peer drops its lost tombstone for `m` unless the
  sender is `m` itself. A peer that was offline during the re-add keeps its lost tombstone and
  wins it back on its next sync; run `yonder add <m>` again once it is online.
- Lost-key cleanup strips only keys in both the tombstone and the record this machine held for
  that name, never a key any live record lists. A machine that never held the record strips
  nothing through sync; `yonder remove <m> --lost` run there still strips the tombstone's keys.
- Output, the log and `jobs/<id>.json` scrub secret values, command text included. Values that
  overlap are replaced as one span named after all of them in order, like `[A+B]`, so no part
  of either shows.
