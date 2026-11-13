# Yonder design

Yonder connects a person's own machines (one to five, macOS and Linux) so the AI agents on any of
them can see and use all of them. One Go binary, `yonder`, installed on every machine. No daemon,
no server, no account. Tailscale carries the network; plain OpenSSH carries the commands.

## Principles

- Plain SSH over the tailnet. Yonder adds a registry, discovery, a log and a skill. Connecting the
  machines is always on; secrets and agent config sync are optional layers on top
  (see Layers).
- Agents handle secret names. Values stay out of command lines, logs and accidental output.
  This does not stop an agent that deliberately reads, transforms or prints a value (see
  Secrets).
- One owner. Yonder assumes a tailnet of one person's machines, with no nodes shared in from
  another tailnet.
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
internal/remote/       the hidden _exec / _send / _recv / _facts / _sync / _log commands run on the target
internal/runlog/       JSONL command log
internal/discover/     hardware, models, harnesses
internal/secrets/      providers (1password, bws) and output scrubbing
internal/agents/       embedded SKILL.md and per-harness install; the agents store's rules, import and harness links
internal/store/        store sync: manifests, version counters, bundles, history, review
internal/doctor/       checks
internal/mesh/         which fleet keys each machine's authorized_keys should hold; the sh that edits a peer's
internal/version/      version string, set with -ldflags
skill/yonder/SKILL.md  the agent skill (embedded into the binary)
test/                  end-to-end tests against the built binary (+ docker integration)
```

The hidden commands a caller runs on a target are `_exec`, `_job` (the detached supervisor),
`_jobs [id [--follow]]`, `_kill <id>`, `_send` and `_recv` (see Copying files), `_sync`, `_log`, `_facts`, `_init-self <name>` (prints `{"mesh_keys":[...]}`), `_store-offer`, `_store-send` and `_store-recv <store>` (see Store sync), and `_secret-probe [--verify]` and
`_secret-provision <account> <vault> [--write] [--extra-vault V]...` (both run through `_exec`;
see Secrets).

## Files on each machine

```
~/.config/yonder/config.toml           this machine: self = "<name>", secrets provider settings
~/.config/yonder/machines/<name>.toml  one record per fleet machine (a copy on every machine)
~/.config/yonder/removed/<name>.toml   tombstones, so refresh never re-adds a removed machine
~/.config/yonder/secrets.toml          NAME = "op://vault/item/field" references, never values
~/.config/yonder/layers.toml           fleet-wide layer settings (synced with the registry)
~/.local/share/yonder/agents/          the agents store (see Agents store)
~/.local/state/yonder/sync/<store>.json         per-file versions, deletes, origins, last sync per machine
~/.local/state/yonder/sync/<store>.log.jsonl    changes this machine recorded, with where each came from
~/.local/state/yonder/sync/history/<store>/     replaced and deleted versions
~/.local/state/yonder/sync/pending/<store>/     incoming changes waiting for review
~/.local/state/yonder/sync/last.log             output of the last `yonder sync --background`
~/.local/state/yonder/agents/links.json         what `agents link` made and what it replaced
~/.local/state/yonder/agents/backup/<ts>/       files and folders link moved out of the way
~/.local/state/yonder/log.jsonl        commands this machine received
~/.local/state/yonder/jobs/<id>.log    output of detached runs (scrubbed)
~/.local/state/yonder/jobs/<id>.json   detached run status: pid, pgid, state, exit
~/.local/state/yonder/lost-keys-applied  lost tombstones already stripped here: "<name> <removed_at>[ shared]"
```

`XDG_CONFIG_HOME`, `XDG_STATE_HOME` and `XDG_DATA_HOME` are honoured. `YONDER_HOME` overrides them
(tests): config goes in `$YONDER_HOME/config`, state in `$YONDER_HOME/state`, stores in
`$YONDER_HOME/data`, and the files yonder touches
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
ssh_client_keys = ["ssh-ed25519 AAAA... me@gpu-box"]  # every ~/.ssh/*.pub there, plus mesh_keys: what remove --lost strips
mesh_keys = ["ssh-ed25519 AAAA... me@gpu-box"]  # the keys it offers when it sshes out, as it reported them: what mesh grants
role = "gpu"                  # optional, user-given: laptop | desktop | server | gpu | other
notes = "sleeps at night, heavy jobs only"  # role and notes change with `yonder set`
secrets_account = "yonder-gpu-box"  # optional: the 1Password service account its token belongs to
secrets_write = true          # optional: that account may also write the agent vault
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
machine's `ssh_client_keys` and `secrets_account`, `lost = true` after `remove --lost`, and
`include_shared = true` after `remove --lost --include-shared`. It also keeps `held_keys`:
the `ssh_client_keys` of the record this machine held for that name when the tombstone replaced
it. `held_keys` stays local and is never synced.

Records merge by `updated_at`, newest wins, per file; the merged record keeps the latest
`added_at` of the two. A tombstone drops the record unless the record's `added_at` is at or after
`removed_at` (the machine was re-added), in which case the tombstone is dropped. `updated_at` does
not count here: a removed machine that comes back and refreshes its own record gets a newer
`updated_at` but keeps its old `added_at`, so the tombstone still wins and it can't resurrect
itself. On equal `removed_at` the lost tombstone wins. A lost tombstone always drops the record,
whatever its `added_at`; only a local `yonder add` clears it (see Trust model).

`config.toml` holds `self = "<name>"`, a `[secrets]` table for provider settings, optional
`[ssh.<name>]` tables (see Per-machine ssh key), and optional hand-edited `[discover]` (see
Discovery) and `[sync]` (see Agents store) tables. Yonder rewrites only `self`, `[secrets]` and
`[ssh.*]` and keeps every other key.

## Reaching a machine

1. Look up the record. If it is this machine, run locally with no SSH.
2. Ask `tailscale status --json` for the peer behind the record: among peers with the record's
   name, the one with its `dns_name`, else the one at its `tailscale_ip`, else the only one. If
   the peer is `Online: false`, stop: exit 75,
   `yonder: gpu-box is offline (last seen 2h ago) — notes: sleeps at night`.
3. The record's `tailscale_ip` must be the peer's current tailnet address. If not, stop: exit 1,
   `yonder: gpu-box's address changed (record X, tailnet Y); run 'yonder doctor'`. Yonder never
   moves a peer's record to a new address on its own; `yonder remove <m> && yonder add <m>` does.

Steps 2 and 3 are one check (`tailnet.Status.Reach`) that every ssh call to a recorded peer goes
through first: `run`, `jobs`, `log`, sync, `agents`, `doctor`, secrets provisioning, `refresh`
(pending ones included) and `remove`. Sync and `log` skip a moved peer with a warning instead of
failing.
4. `ssh -T -o BatchMode=yes -o ConnectTimeout=5 -o Port=<port> -o User=<user> <tailscale_ip>
   <yonder_path> _exec`, with the host key pinned (below). SSH exit 255 means unreachable:
   exit 75, `yonder: gpu-box is unreachable over ssh (<ssh's error>) — notes: ...`.

A remote command that itself exits 255 is indistinguishable from an ssh failure and is
reported as unreachable. Commands over ssh start in the target user's home; local runs start
in the current directory.

Host key pinning: every ssh, scp and ssh-copy-id call gets a fresh temp `known_hosts` holding
only `yonder-<name> <ssh_host_key>`, plus `HostKeyAlias=yonder-<name>`,
`GlobalKnownHostsFile=/dev/null` and `StrictHostKeyChecking=yes`, and `ControlMaster=no
ControlPath=none`, so a connection-sharing socket from the user's `~/.ssh/config` can't skip the
check. The user's own `known_hosts` is never read or written. An explicit `yonder add` (or `init`
picking a peer) is the one trust moment: it connects with `StrictHostKeyChecking=accept-new`
against an empty temp file and reads the key back. If the record already held a different key,
it warns that the key changed and names `yonder remove <m> --lost`. `refresh` finishing a
pending record that still holds a pin probes strictly against that pin, and never accepts a new
key; a mismatch stops with that advice. Nothing else uses `accept-new`: `refresh`, `ls` and
`doctor` never finish a pending record without a held pin (one saved by an offline `add` or
`init`, or one that arrived through sync). They report "<m> is online now; run `yonder add <m>`
to finish setup" instead, and `refresh` of all machines still exits 0. A machine's own record takes its key from
`/etc/ssh/ssh_host_*_key.pub`.

`YONDER_SSH_IDENTITY=<private key>` adds `-i <key> -o IdentitiesOnly=yes` (tests and CI).

### Per-machine ssh key

Some people reach a peer only with one specific key (`ssh -i ~/.ssh/id_ed25519_gpubox me@...`)
and have no ssh config for its tailnet address. Which key is a property of the caller, since each
machine has its own, so it lives in this machine's `config.toml` and never in the synced record:

```toml
[ssh.gpu-box]
identity = "~/.ssh/id_ed25519_gpubox"   # ~ stays unexpanded in the file, expanded at use
```

For that target, every ssh, scp and ssh-copy-id call gets `-i <key> -o IdentitiesOnly=yes`. It beats
`YONDER_SSH_IDENTITY`, which stays the fallback for every target. Set it with
`yonder add <m> --identity <key>`, `yonder init --identity <m>=<key>` (repeatable), or later with
`yonder ssh-key <m> [path | --clear]` (no path prints it). `add` and `init` check the file exists and
store the key once ssh works with it. When key auth fails in a terminal, `add` asks "Which key do you
use for <m>? (Enter to skip)" before offering ssh-copy-id; without a terminal the error names both
fixes. `doctor` FAILs for a configured key whose file is missing, and its ssh fix mentions
`yonder ssh-key`. `ssh-key` is its own small command rather than part of `add`, because changing the
key should not re-probe or re-pin the machine.

Tailscale cannot tell a sleeping machine from a switched-off one, so Yonder says "offline" and
shows last seen plus the user's notes. It does not guess.

`YONDER_TAILSCALE_JSON=<file>` replaces the live status with a fixture (tests and CI).

## The `_exec` envelope

The caller writes one JSON line to the remote `yonder _exec` on stdin, then passes its own stdin
through. The envelope holds the command, the caller's machine and harness, the secret names and
their values, and whether to detach. Values never appear in argv, so they never show up in `ps`
or shell history. The target:

1. runs the command with `$SHELL -lc '<cmd>'`, secrets added to its environment. Provider
   credentials are removed from the environment it inherits first: `OP_SERVICE_ACCOUNT_TOKEN`,
   `BWS_ACCESS_TOKEN`, `YONDER_OP_TOKEN`, `OP_CONNECT_TOKEN`, `OP_CONNECT_HOST` and any
   `OP_SESSION_*`. A `--secret` of the same name is added back with the resolved value. `_job`
   does the same.
2. scrubs every secret value out of stdout and stderr (replaced with `[NAME]`; overlapping
   values become one `[A+B]`). The exact value is caught at any length. It is also caught
   trimmed and line by line, each only when 8 or more characters (shorter would match ordinary
   output), and JSON-escaped. A value the command transforms (reversed, cut, encoded)
   is not caught.
3. before running it, appends a start entry to its log: time, id, from, to, harness, user,
   command (secret values scrubbed), secret names, `"status":"started"` and `"exit":null`, so a
   run cut off midway (caller gone, ssh dropped, target killed) still shows up
4. after it, appends the completion entry: the same fields with exit and ms, and no `status`
5. exits with the command's exit code

Detached runs use `setsid`/`nohup`, write output to `jobs/<id>.log`, and return the id at once.
Concretely, `_exec` re-runs its own binary as `yonder _job` in a new session (`Setsid`, SIGHUP
ignored), hands it the envelope on a pipe, logs the launch with `"exit": null`, prints the id,
and exits 0. `_job` runs the command in its own process group with output scrubbed into the log
file, and writes `jobs/<id>.json` once the command has started: `pid` is the `_job` supervisor,
`pgid` the command's group. A job whose pid has gone while still marked running shows as `lost`.

`yonder jobs <m>` lists the jobs, and `yonder jobs <m> <id>` prints one job's log so far.
`--follow` (`_jobs <id> --follow`) prints the log, keeps printing what is added every 200 ms, and
when the status says `exited` it prints the rest and exits with the job's code (`lost`: exit 1
with a note). It reads the status before each read of the log, and `_job` writes `exited` only
after the command's output is in the file, so nothing is cut off. Stopping the follow leaves the
job running. A job whose status file is not there yet counts as starting, for up to 10 seconds.

`--kill` runs `_kill <id>` with an envelope line on stdin, so the target can log who asked. It
sends SIGTERM to the command's process group, waits up to 10 seconds for the group to empty, then
sends SIGKILL to whatever is left. The supervisor is outside that group, so it survives and
records the exit (143 for SIGTERM, 137 for SIGKILL). A job from an older yonder has no `pgid`;
its supervisor leads its own session, so that group is signalled instead, and `_kill` writes the
`exited` status itself. A job that is not running is refused with exit 1. The target logs one
entry per kill attempt, command `kill job <id>: <job command>`, exit 0 or 1 with `error`. An
older target gets "too old for jobs --follow" (or `--kill`); upgrade it there.

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

A copy adds `"copy":true`, and its completion entry adds `files` and `bytes` (see Copying
files). `side` is `target` on the machine that ran the command. When the caller cannot reach the
target (offline, unreachable) it logs the attempt locally with `"side":"caller"` and
`"exit":75`. Detached launches add `"detached":true` with `"exit":null`. Failures add `error`.
Secret values never appear in the log. `yonder log` merges every online machine's `_log` with
the local file, pairs each start entry with the completion of the same id and target (one row
per run), dedupes and sorts by time. A start with no completion stays as one row with exit `?`
and the note "no completion logged: still running, or cut off"; in `--json` it keeps
`"status":"started"` and `"exit":null`.

## Copying files

`yonder cp [--force] [-r] <src>... <dest>` copies files and folders like scp. Each side is a local
path or `<machine>:<path>`; a side is remote when the text before its first `:` has no `/` and
looks like a machine name, so `./a:b` is a local file. The named machine must be in the registry.
All sources are on one machine. At least one side names a machine (it may be this one). An empty
remote destination path means `~/`; an empty source path is a usage error.

Folders copy whole, with or without `-r` (accepted for scp habit). A Keynote file, an app bundle or
a project folder needs no flag, and agents can't forget one. A source that is itself a symlink is
followed; links inside a folder are copied as links. Only files, folders and links travel:
sockets, devices and pipes are skipped with a warning. Permission bits (without setuid, always
owner-writable) and file mtimes are kept; owners, extended attributes and ACLs are not.

Placement follows cp. If `<dest>` ends in `/` or is an existing folder, each source goes inside it
under its own name; a missing folder ending in `/` is created. Otherwise the single source is
copied to that name, and its parent folder must exist. An existing file or folder at the final
path is refused unless `--force`, which replaces it whole (a folder is not merged).

Paths: `~` expands on the machine that reads the path (under `YONDER_HOME`, to
`$YONDER_HOME/home`). A relative remote path is under that machine's home, as with scp. Local
paths are made absolute here before they are sent, so the log shows full paths.

Transport: the caller pipes `yonder _send` on the source machine into `yonder _recv` on the
destination, each run through the usual runner: in process for this machine, ssh with the pinned
host key for a peer. Both machines go through `Reach` first (offline: exit 75; address changed:
refused). Between two other machines the data is relayed through the caller, since peers don't
necessarily trust each other's keys (see Mesh). Both commands take the `_exec` envelope line first, with a `copy`
object (`sources`, `dest`, `force`) and the text `cp <m>:<src>... -> <m>:<dest>` as `command`;
`_exec` refuses an envelope with `copy` set. A tar stream over the existing ssh channel was picked
over scp or sftp: it reuses the pinned transport and the envelope as is, behaves the same whatever
the peer's sftp subsystem or scp version, lets the receiver check every entry before it touches
disk, and lets the receiver write the log.

`_send` checks every source before sending a byte, then writes a PAX tar with no owner names or
ids. Its last entry is `.yonder-cp-done`, holding `{"files":N,"bytes":B}`.

`_recv` unpacks into a hidden `.yonder-cp-*` folder in the folder that will hold the result,
opened as an `os.Root`, so no entry can land outside it. It refuses the whole copy for an entry
name that is absolute or has an empty, `.` or `..` part, for an entry whose parent is not a folder
from the same archive (so nothing is written through a link), and for entries after the end
marker. It skips, with a warning, a link that is absolute, has a `..` after a normal part (that
part could itself be a link, which a lexical check can't follow), resolves outside its own
top-level item, or is a top-level item itself. It checks each top-level item's final path for a
conflict when the item's first entry arrives, before reading its contents. Without the end marker,
or with counts that differ from it, it keeps nothing and exits 74 (the caller then reports the
sending side's error, or 75 if the sender was unreachable). Then it renames each item into place;
with `--force` the old one is moved aside first and put back if the rename fails. It prints one
JSON line, `{"files","bytes","items":[{"path","dir"}],"skipped":[...]}`, and the caller prints
`copied Talk.key (folder, 23 files, 4.2 MB) to laptop:/Users/me/Desktop/Talk.key`. A copy killed
midway can leave a `.yonder-cp-*` folder behind; nothing else.

The receiver logs the copy like `_exec`: a start entry, then a completion with `files`, `bytes`,
`exit` and `ms`, `from` the source machine and `to` itself. The sender logs nothing. A machine
that can't be reached is logged on the caller with `"side":"caller"`, `to` that machine and exit
75. An older yonder without `_send` or `_recv` gets "too old for cp; upgrade it there".

## Adding machines

`yonder add <m>` requires `m` to be a tailnet peer (not iOS/Android), named by its short name or
its full DNS name. When two peers share a short name (a node shared in from another account can
carry the same name as yours) the short name is an error that lists their DNS names and asks for
the full one; the record still takes the short name. A peer with a record here whose `dns_name`
differs is refused (`yonder remove <m>` first). A peer the logged-in tailscale account doesn't own
(its `UserID` differs from `Self`'s, or it is a `ShareeNode`) is refused unless `--allow-shared`
is given. The check fails closed: if `Self` or the peer has no `UserID` (missing or 0), the peer
is not owned. A tagged node (`Tags` set) is never owned, even when its `UserID` matches `Self`'s:
Tailscale keeps the creator's `UserID` on a tagged node, but its identity is the tag; the refusal names its tags, says tagged devices count as not yours, and gives
`yonder add <dns> --allow-shared`. With `--allow-shared` it is confirmed first when a terminal is attached, since adding it trusts it with ssh and can hand it a
secrets token. It probes over ssh with
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
`_init-self <m>` there and keeps the `mesh_keys` it reports (see Mesh), and syncs the registry to every online machine. An offline peer is
saved as `pending = true` and `add` exits 75; `yonder add <m>` finishes it once it is online
(the message says so). If the record here was already set up, the pending record keeps its
`tailscale_ip`, `ssh_host_key` and `ssh_client_keys`, and `refresh` finishes it against that pin.
A pending record with no pin is never finished by `refresh`; it prints "<m> is online now; run
`yonder add <m>` to finish setup". `refresh`
refuses to finish a pending peer the account doesn't own (fix `yonder add <dns> --allow-shared`).
A set-up record with no `ssh_host_key` is an error with fix `yonder add <m>`; `refresh` never
pins a new key for one.

### Changing role and notes

`yonder set <m> [--role R] [--notes TEXT]` changes those two fields and `updated_at`, nothing
else. An empty value clears one; a role outside the list above is a usage error, and so is giving
neither flag. `set` was picked over `edit`, which reads like opening an editor.

Sync never lets a peer change a machine's own record (see Trust model), and a machine refreshing
itself keeps the role and notes it holds, so a change made only here would be lost. So for a set-up
peer, `set` runs `yonder set <m> --here <flags>` on that machine through `_exec` (it logs it), which
changes its own record and nothing more, then syncs as usual: the peer's reply brings the record
here and the second pass sends it on. A peer that is offline or unreachable is an exit 75 and
nothing changes. For this machine, and for a pending peer (it holds no record of itself yet, and
`add` finishes it with these values), the record is changed here and synced. It prints
`set <m>: role server, notes "always on"`. An older peer without `set` gets "too old for set;
upgrade it there".

`yonder init --yes` sets up only this machine unless `--machines a,b` names peers (short or full
names, resolved like `add`). Its peer list shows a peer the account doesn't own with a note and
leaves it out; with `--allow-shared` it is listed, marked as not yours, and asks again when picked.
Nothing in the interactive list starts selected. After adding
peers, init also installs the skill on each machine it added that is online and set up (the
`agents install --all-machines` path), asking once "Install the skill on <names> too?" unless
`--yes`, and prints one line per machine. Failures there are warnings.

After a successful `add`, and after `init` added at least one peer, yonder offers the mesh step
(see Mesh): "Let these machines ssh into each other?", default yes. `--yes`, or no terminal,
takes the default; `--no-mesh` skips it. `add` meshes only the pairs that involve the new
machine; `init` meshes the whole fleet. A mesh failure is a warning, since the add itself worked.

Sync: the caller sends its bundle (`{"machines":[...],"tombstones":[...],"clear_lost":[...],"layers":{...}}`) to
each online machine's `_sync`, merges each reply as coming from that machine (same rules as
`_sync`, see Trust model), saves, then resends to any machine whose reply was missing something.

Lost-key cleanup rides on sync. After merging, `_sync` looks at every `lost = true` tombstone
it now holds, received or already there, and strips that machine's keys from its own
`~/.ssh/authorized_keys` with the same code as `remove --strip-keys` (same match rules, same
backup, and the same refusal to write through a symlink). It strips only keys in both the
tombstone and its `held_keys`, and never a key that any live record (self included) lists,
unless the tombstone has `include_shared`. Between two lost tombstones with the same
`removed_at`, the `include_shared` one wins the merge. The caller does the same on itself after every sync (`refresh`,
`add`, `remove`), even with no peer online. Each tombstone is applied once per machine and
recorded in `lost-keys-applied` (an `include_shared` tombstone under its own id, `<name>
<removed_at> shared`, so an upgrade of one already applied strips the shared keys), so later
syncs don't touch the file, and a key the user adds
back by hand stays. A failed strip is not recorded, so the next sync retries it. It never
fails the sync; `doctor` reports a key left behind.

## Mesh

`add` copies a key from the machine it runs on, so only that machine can ssh into the new one.
`yonder mesh [<m>] [--yes] [--allow-shared]` closes the gap: every online machine's
`~/.ssh/authorized_keys` gets the `mesh_keys` of every other fleet machine, and loses the lines
mesh tagged earlier that are no longer granted. With `<m>`, only the pairs that involve `m`: `m`
gets everyone's keys, everyone else gets `m`'s, and only lines tagged for those pairs are pruned.
It prints the plan (per machine, whose keys it adds and whose it removes) and asks once, default
yes; `--yes` or no terminal takes it. Offline machines are named, to mesh later. Exit 1 if any
machine could not be updated.

What it grants is `mesh_keys`, not `ssh_client_keys`. `add` records every `~/.ssh/*.pub`, which is
right for stripping a lost machine's keys (be thorough when revoking) but wrong for granting: a
`.pub` on a machine doesn't mean that machine is the only holder of the private key (a sandbox key,
a deploy key, a key copied from elsewhere). `mesh_keys` is what the machine itself reports from
`_init-self` at add time: the `.pub` of each of ssh's default identities that exists with its
private key (`id_rsa`, `id_ecdsa`, `id_ecdsa_sk`, `id_ed25519`, `id_ed25519_sk`, ssh's order), then
the `.pub` of every key set with `yonder ssh-key` there. Keys set only through `~/.ssh/config`
`IdentityFile` are not included. The caller keeps at most 32, each as `type base64` plus a
printable comment, and adds them to `ssh_client_keys` too, so a lost machine's strip covers every
key mesh granted. This machine's own `mesh_keys` are read fresh before each mesh run, on `refresh`
of itself, and when `yonder ssh-key` changes them. Other machines hold their own copy, which sync
doesn't change (Trust model): after a key change there, `yonder add <m>` from each other machine
takes the new keys. A record with no `mesh_keys` (added by an older yonder, or a machine whose ssh
has no default key) gets nothing granted, and mesh names the fix: upgrade yonder there, or make
`~/.ssh/id_ed25519` or set `yonder ssh-key` there, then `yonder add <m>`.

Keys come only from records this machine holds (Trust model): set up, with a pinned host key (so
sync can no longer change their keys), no tombstone, and still the tailnet node they were added as
(the peer behind the record is at the recorded `tailscale_ip`). A machine the tailscale account
doesn't own is left out unless `--allow-shared`. A key any lost tombstone lists (`ssh_client_keys`
or `held_keys`) is never handed out, so a later mesh can't re-trust a key `remove --lost
--include-shared` took away. Each key is written as its `type base64` only, never with options or
the original comment, and a type or base64 with any other character is skipped.

Each line added is `<type> <base64> yonder:<machine>`; the tag is how mesh and `remove` find it. A
line with no tag (the user's own, or one `ssh-copy-id` wrote) is never touched, and a key already
in the file is not added again, wherever it sits (with options, under another tag, or commented
out with `#`, so a key the user disabled by hand stays disabled). A tagged line is pruned when its
key isn't in that machine's grant set: the key is no longer among its `mesh_keys`, the machine
was removed or its address changed, the tag names the file's own machine, or no record has that
name. Lines tagged for a machine mesh can't judge right now are kept: setup unfinished, not in
tailscale status, not owned without `--allow-shared`, or no `mesh_keys` on record (so running mesh
from a machine whose copies predate `mesh_keys` can't undo what another machine granted). Pruning
runs first, then the missing grants are appended, so a key moved from one tag to another is
retagged. A run with nothing to change writes nothing. Otherwise the original goes to
`authorized_keys.yonder-bak.<unix-nanos>` first, as with `remove --lost`, and the file keeps its
mode; a missing file is created 0600 in a 0700 `~/.ssh`. If `~/.ssh/authorized_keys` or `~/.ssh`
is a symlink, nothing is written and each change is printed to make by hand (`+ line` to add, `-
line` to delete).

This machine's file is edited in Go (`transport.EditAuthorizedKeys`, the same code as the strip).
A peer's is read and replaced with plain `sh` over the pinned ssh connection, not a yonder
subcommand, so the edit works whatever yonder version runs there. The read prints the file's
`cksum`, whether it is a symlink, and its content; yonder computes the new content; the write
script refuses a symlink, refuses (exit 4) if the `cksum` changed since the read, makes the
timestamped backup, and renames a temp file into place. A changed file is read and tried once
more.

## `ls --json` and `show --json`

Agents parse these. Keys are always present; new keys may be added, none removed or renamed.

```json
{
  "self": "laptop",
  "tailscale_error": "",
  "layers": {"secrets": true, "agents": false},
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
spreads them. Pending and offline machines are skipped. Under the table, `ls` prints "<m> is
online now; run `yonder add <m>` to finish setup" for each online pending machine without a pin. `--no-refresh` prints the stored
facts as they are. The 7-day `stale` marker still applies to whatever is printed.

Discovery (`internal/discover`) runs on the machine itself, all probes concurrent, each with its own
timeout (about 3 s total). A missing tool is absent from the facts, never an error. Every
discovered string (model names, runtimes, harness versions and paths, CPU and GPU names, OS
version, hostname) loses its control and format characters (escape sequences, bidi overrides,
zero-width) and is cut to 120 characters (paths to 512). A model whose name is left empty is
dropped. The caller runs the same cleaning again on what a peer's `_facts` returns (OS and arch
too), and drops `facts.extra` from it, before saving; a peer's reply is outside input. Agents
still read these strings; the skill tells them to treat them as data.

- Hardware: `sysctl`, `sw_vers`, `pmset -g batt`, `system_profiler SPDisplaysDataType` (Intel
  Macs only; Apple silicon reports the chip as one unified-memory GPU), `/proc`, `/etc/os-release`,
  `/sys/class/power_supply/BAT*`, `nvidia-smi`, `rocm-smi`.
- Models: Ollama (`:11434/api/tags`, else `ollama list`), LM Studio (`:1234/v1/models` and
  `lms ls --json`), OpenAI-compatible servers on `:8080` and `:8000` (llama.cpp, vLLM, MLX,
  llama-swap). `source` is `api` or `cli`. `serving` is true when a local server listed it, with
  its `port`. Runtime is guessed from `owned_by` for the unnamed servers (`llama-swap`,
  `llamacpp`, `vllm`, `mlx`, else `openai-api`). A server on another port is found only when
  that machine's `config.toml` names the port; yonder reads it and never writes it:

  ```toml
  [discover]
  model_ports = [9292]   # more 127.0.0.1 ports to ask for /v1/models; up to 8 are used
  ```

  Ports already probed, and ports outside 1-65535, are ignored. The setting lives on the
  machine that serves the models, since discovery runs there.
- Harnesses: each binary is searched on `PATH` plus `~/.local/bin`, `/opt/homebrew/bin`,
  `/usr/local/bin`, `~/.npm-global/bin` and `~/.bun/bin`; version is the first version number in
  `<bin> --version` (2 s timeout). Every probed tool runs with `PATH` set to its own directory,
  then the inherited `PATH`, then those directories, because an ssh shell's bare `PATH` breaks a
  `#!/usr/bin/env node` script installed beside its `node`. A mise shim (a `shims` directory
  under a `mise` directory) is never run, since running one installs a missing tool: yonder asks
  `mise which <bin>` for the real binary and runs that. If mise says the tool isn't active, the
  harness is left out; if no `mise` binary is found, the harness is listed without a version.

`show <m> --json` prints one machine object with one extra key, `record`, holding the full
record (including `yonder_path`, `ssh_host_key`, `ssh_client_keys`).

## Exit codes

- the remote command's own code, passed through
- 75: target offline or unreachable (EX_TEMPFAIL); agents should tell the user, not retry
- 74: only from `_recv`, when the stream ended early; `yonder cp` itself exits 1 or 75 then
- 2: usage error
- 1: any other yonder error

## Layers

Connecting the machines (registry, discovery, `run`, `cp`, `jobs`, `log`, `mesh`, `doctor`, the
skill) is always on. Two layers sit on top, each optional and fleet-wide:

- `secrets`: `--secret`, `secret read`, `test` and `set` (see Secrets).
- `agents`: the agents store and its sync (see Agents store).

There is no memory layer. General agent memory stays in each agent's own memory.

`yonder enable <layer>...` and `yonder disable <layer>...` change them; `yonder layers [--json]`
shows them (`{"layers":[{"name","on","set","by","updated_at","summary"}]}`, `set` false
when nobody has set it). `init` asks with a multi-select (the current state starts ticked), or takes `--layers secrets,agents` (`none` for none); `init --yes` without the
flag keeps what is set. Only layers whose value changes are written, so an unchanged one keeps
its date. `ls` prints one line under the table (`layers: secrets on, agents off`) and `ls --json`
has `"layers": {"secrets": true, "agents": false}`. `doctor` has a `layers` line.

The setting lives in `layers.toml`, one table per layer: `on`, `updated_at`, `by` (the machine that
set it). It rides in the registry bundle as `"layers"`, merged per layer, newest `updated_at`
wins, with the same 5-minute skew guard as records. An older yonder ignores the key and leaves it
out of its reply, which never clears the setting here. `SetLayer` always dates a change after the
one it replaces, even within one second. A name this version doesn't know (a newer yonder's
layer, or a hand-written one) is never on here, is refused by `enable`, and still passes through
sync unchanged.

A layer nobody has set uses its default: `secrets` is on where `[secrets] provider` is set, so a
0.1 fleet that used secrets keeps them, and `secret setup` on a fresh machine turns it on unless
someone turned it off. `agents` starts off.

Turning a layer off stops it and keeps its data:

- `secrets` off: `run --secret`, `secret read`, `secret test` and `secret set` fail with
  "the secrets layer is off, so --secret is refused; turn it on with `yonder enable secrets`".
  `secrets.toml`, provider settings and tokens stay. `doctor` skips the provider checks.
- `agents` off: every machine unlinks its harnesses (`agents unlink`, which puts back what link
  replaced) and marks its links paused; `_store-*` there refuse with exit 3 and the caller skips
  that machine ("the agents layer is off there"). The store and its history stay. A machine
  applies the setting when it arrives: in `_sync` on a peer, after the caller's own sync. Turning
  it back on links each paused machine again with the harnesses it had.

## Secrets

`secrets.toml` maps names to references. With 1Password, a name can also be an item title in the
agent vault (below). The machine the agent runs on resolves the values with its configured provider,
then sends them inside the envelope over SSH. Values live only in that yonder
process's memory, the envelope on ssh stdin and the remote command's environment. Never in argv,
logs or errors, and scrubbed from output. The remote command's environment can be read by other
processes of the same user on that machine (for example through `/proc/<pid>/environ` on Linux).

What this protects, exactly:

- Protected: command lines and `ps` as far as yonder's own transport goes (a tool the command
  runs can still put a value in its own argv), the log and job files, and output that happens to
  contain a value (exact-match scrubbing, plus the trimmed and per-line forms of 8+ characters
  and the JSON-escaped form). Provider
  error text that could quote a value or token is never shown (below).
- Not protected: an agent that deliberately reads, transforms or prints a value
  (`echo $V | rev`). Nor anything the agent can read as the user: the token store (keychain
  entry or token file), other processes' environments, `op` itself in an approved desktop
  session.
- Scope: in desktop mode, one approval reaches any vault the signed-in 1Password session can
  read, since `secret add` accepts any `op://` reference. In service-account mode it reaches
  only the vault granted to that account. bws reaches what the machine account's projects hold.

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
service_account = "yonder-gpu-box"  # service-account mode, set by provisioning: names, never secrets
write = true                  # service-account mode: the account may write the vault
vault = "Yonder Agents"       # 1password, either mode: the agent vault
extra_vaults = ["Ops"]        # 1password, optional: more vaults to read names from, after vault
```

`vault` is set by provisioning (on each target, and on the machine running it when that one stays
in desktop mode with no vault yet), or by hand with `yonder secret setup --provider 1password
[--mode m] --vault NAME`. `--vault` alone still means provisioning. `--provider 1password` without
`--mode` keeps the current mode.

`extra_vaults` is set by provisioning with `--extra-vault` (below), or by hand with
`yonder secret setup --provider 1password --extra-vault NAME` (repeatable; needs an agent vault;
a hand setup without the flag keeps the list). Names are checked like `vault`; repeats and the
agent vault itself are dropped. They hold secrets only some machines may read: a machine reads
them, it never writes them, and `secret set` never stores there. A vault no service account can
read (say one kept for Touch ID only) needs no entry here; the machine with the app reaches it
through a `secrets.toml` reference like `op://Gated/item/field`.

Providers:

- `1password` with `mode = "desktop"`: `op` with the desktop app integration (Touch ID, Apple Watch,
  system auth). The approval lasts until 10 minutes idle or 12 hours. `OP_SERVICE_ACCOUNT_TOKEN` is
  removed from `op`'s environment in this mode.
- `1password` with `mode = "service-account"`: hands-free. A service account per machine, read-only
  unless provisioned with `--allow-write`, scoped to one dedicated vault (service accounts cannot see Personal, Private or the default Shared
  vault). Individual and Families plans allow 1,000 reads a day.
- `bws`: Bitwarden Secrets Manager with a machine-account access token.

1Password cannot approve CLI reads from its phone app today. When it can, that becomes a third mode.

`yonder run --secret A --secret B` makes one provider call for all names. 1Password gets one
`op inject` with a template on stdin: each reference sits between random boundary lines, so a value
may hold newlines and still parse back. bws gets one `bws secret get <uuid> --output json` per name.
Unknown names fail before any provider call and list the known names.

Resolution order for a name: `secrets.toml` first, then, with 1Password and a recorded `vault`, the
item in that vault titled exactly that name, then the same in each of `extra_vaults` in order. The
first vault that holds the title wins, so an extra vault can't shadow an agent vault item. Names all
found in `secrets.toml` never touch a vault. For the rest, yonder runs one `op item list --vault V
--format json` (titles, ids and categories, no values) per vault, and lists the next vault only
while names are left. The field is picked by category where the layout is fixed: `API_CREDENTIAL` reads
`credential`, `PASSWORD` and `LOGIN` read `password`, `SECURE_NOTE` reads `notesPlain`. Those become
`op://<vault id>/<item id>/<field>` references and join the `secrets.toml` references in the same
single `op inject`. An item of any other category costs one `op item get --format json --reveal`,
whose fields are searched in the order `credential`, `password`, `notesPlain`, then the first
`CONCEALED` field; the value stays in memory. No such field, or two items with the same title, is an
error. So a typical batch is two calls: one list, one inject. Ids, not titles, go in the references,
so odd vault names can't break the `op://` syntax. A per-name `op inject` with
`op://V/NAME/credential` would need the field known up front and fails on any other category; one
`op item get` per name would be one read (and one rate-limit hit) per name.

`yonder secret set NAME [--replace]` stores a value in the agent vault. The value comes from stdin
when stdin is not a terminal (one trailing newline dropped), else from a hidden prompt. Empty values,
values over 64 KiB and non-UTF-8 values are refused. It lists the vault once; with no item titled
NAME it runs `op item create --vault V --tags yonder -` with an `API_CREDENTIAL` template on stdin
(`title`, `category`, and the value in the concealed `credential` field). An existing title is
refused unless `--replace`, which reads the item with `op item get`, sets the field the resolution
rule picks, and pipes the whole item JSON back to `op item edit <id> --vault <vault id>`. Two items
with the title are an error. It prints only `stored NAME in <vault> (N chars)`. In service-account
mode, a permission error from op becomes: this machine's service account is read-only; run
`yonder secret setup --allow-write --machines <self> --replace` from a machine with the 1Password
app, then revoke the old account (named). bws is refused: `bws secret create` takes the value as an
argument. Any other create or edit failure is a fixed text, like `1Password could not create the
item (op exit N); run 'op item create --help' or check that this machine can write to the vault
"V"`. op's stderr is never quoted there: it could echo the template, and an edit's template holds
every field of the item.

Provider errors elsewhere follow one rule. `op inject` and `bws secret get` failures are fixed
texts with the exit code and a hint, never the provider's stderr. Where a provider line is shown
(`op item list`, `op item get`, `op whoami`, `op vault list`/`create`, `op service-account create`,
token store reads and writes), all of stderr is scrubbed before the last line is cut from it: of
any value or token sent, and of every provider credential in the child's environment
(`OP_SERVICE_ACCOUNT_TOKEN`, `BWS_ACCESS_TOKEN`, `YONDER_OP_TOKEN`, `OP_CONNECT_TOKEN`,
`OP_CONNECT_HOST`, `OP_SESSION_*`), each replaced with `[VAR_NAME]`. Desktop-mode op inherits the
session and Connect variables and could echo them.

Tokens for hands-free modes go to the provider only through the child's env
(`OP_SERVICE_ACCOUNT_TOKEN`, `BWS_ACCESS_TOKEN`). If that variable is already set in yonder's own
environment it wins over the store. It never reaches an `_exec` command (see the envelope).
The store, by OS:

- macOS: the login Keychain, service `yonder`, account `1password` or `bws`. Writing runs
  `/usr/bin/security -i` and sends the `add-generic-password -U ... -w <token>` line on its stdin,
  so the token is never in any process's argv. Tokens are limited to `[A-Za-z0-9._:=+/~-]` so the
  line can't be split. Reading is `security find-generic-password -w`.
- Linux with `secret-tool` and a D-Bus session: `secret-tool store` reads the token from stdin.
- Elsewhere: `~/.config/yonder/tokens/<provider>`, mode 0600. `doctor` names the file and refuses a
  token file other users can read.

Commands: `yonder secret add NAME <ref>`, `set NAME [--replace]`, `ls [--json]` (names from
`secrets.toml` and from vault titles that are valid names, agent vault then extra vaults, never
values; a name shows once, from where it resolves; with extra vaults each vault item names its
vault; in desktop mode the vault list asks for approval like any read),
`rm NAME`, `test NAME` (resolves, prints only `NAME: ok, N chars`), `read NAME` (below), `setup [--provider p --mode m]`
and `token set` (hidden prompt, or stdin: `yonder secret token set < tokenfile`). The manual path
(`setup --provider ... --mode service-account`, then `token set` on each machine) stays for bws and
for people who make service accounts themselves.

`yonder secret read NAME` prints the value to stdout, then one newline, like `op read`. It is for
programs that need a key on stdout, such as an auth helper in a config file
(`auth_command = "yonder secret read MODEL_API_KEY"`). Agents use `run --secret` instead, which
scrubs output. It takes no machine: it resolves here, with the same rules as `run --secret` from
this machine, so a name only an extra vault holds resolves only where this machine's account reads
that vault, and desktop mode may ask for approval. Nothing is scrubbed, since printing the value is
the point. It changes nothing in "What this protects": an agent allowed a secret can already read
it (`run self --secret N -- 'echo $N | rev'`). There is no tty check; one would be easy to get
around and would only get in a person's way. Each read is logged here, side target, from and to
this machine, command `secret read NAME`, `secrets` `["NAME"]`, the harness, and the exit with
`error` on failure. Like `_exec`, it writes a start entry (`"status":"started"`, `"exit":null`)
before resolving, so a read killed or cancelled while it waits for approval still shows in
`yonder log`. The harness comes from the environment yonder inherits, so a read an auth helper
runs for an agent logs that agent's harness: yonder can't tell a program from the agent's shell. A name that isn't valid is a usage error (2) and isn't logged. Exit codes are
those of `secret test`: 1 for an unknown name or any provider error, such as a locked app, a
declined approval or a provider it can't reach. No `secret` command exits 75, which is for
machines.

### Provisioning hands-free 1Password

`yonder secret setup [--machines a,b | --all-machines] [--vault NAME] [--extra-vault NAME]...
[--allow-write] [--replace] [--expires-in 90d] [--yes]` sets up 1Password service accounts for the fleet from the machine a
person sits at, with one approval. Any of those flags starts it; with no flags and a terminal,
setup offers it first, then "choose a provider for this machine only" and "skip". `init` offers
the same choice when this machine has no provider yet; `init --yes` prints the command when `op`
and the 1Password app are installed, without running `op`.

It needs `op` here with a person's session: `op account list` lists an account (the app's CLI
integration, or `op signin`). Otherwise it stops and prints the manual path. It never runs `op`
with `OP_SERVICE_ACCOUNT_TOKEN` set. Steps:

1. Pick machines. `--machines` names them (this machine allowed); `--all-machines` is every fleet
   machine; otherwise every other online, set-up machine, plus this one only if it has no
   1Password app. A terminal gets a multi-select with that default ticked and a prompt for the
   vault, then "Let agents add secrets too?" (default No; they could also change or delete items
   in that vault) unless `--allow-write` is given. Offline, pending or unpinned machines are
   skipped with a note and the command to rerun later: `yonder secret setup --machines <m>`.
2. Check every chosen machine before any account exists, so none is orphaned. Each runs
   `yonder _secret-probe` through `_exec` (this machine: in process), which prints JSON: `os`, `op`
   (path, empty when missing), `op_version`, `desktop_app`, `provider`, `mode`, `has_token`,
   `service_account`, `vault`, `extra_vaults`, `write`, `can_extra_vaults` (this yonder takes
   `_secret-provision --extra-vault`), and with `--verify`, `verified` and `detail`. No values.
   With `--extra-vault`, a machine whose probe lacks `can_extra_vaults` fails with "upgrade yonder
   on <m>" here, before any account exists. A machine
   without `op` is skipped with the install hint for its OS (macOS `brew install 1password-cli`;
   Linux the get-started link). A machine whose yonder lacks the command (exit 2, "unknown
   command") fails with "upgrade yonder on <m>". A machine with a 1Password service-account token,
   or using bws, needs `--replace` or a yes at the prompt; the message names the old account and
   says it keeps working until revoked on 1Password.com.
3. Make the vault (default "Yonder Agents") unless `op vault list --format json` shows it:
   `op vault create <name> --description ...`. This is usually the call that asks for Touch ID;
   later calls reuse the approval. Names a service account can't use (Personal, Private, Employee,
   Shared) and names with `:` or `,` are refused before anything runs. Each `--extra-vault` is
   made the same way if missing, with a description saying only some accounts read it.
4. Per machine: `op service-account create yonder-<m> --vault <vault>:read_items --raw
   [--expires-in d]`, or `<vault>:read_items,write_items` with `--allow-write`, plus
   `--vault <extra>:read_items` for each `--extra-vault`. Permissions are
   fixed at creation, so changing a machine's access means `--replace`. The token is read from stdout into memory. A replacement whose old account
   had the same name gets `yonder-<m>-<yyyymmdd-hhmm>`, so the old one can be told apart and
   revoked. This machine stores it in process. Another machine gets it in the `_exec` envelope as
   the secret `YONDER_OP_TOKEN`, running `yonder _secret-provision <account> <vault> [--write]
   [--extra-vault V]...` (`--write` only when granted, so an older peer still takes a read-only
   token), which reads the env var, stores the token in that machine's token store and sets
   `[secrets]` to 1password service-account with `service_account`, `vault`, `extra_vaults` and
   `write`. So the token is never in argv, and the
   target scrubs it from output and logs only its name. Both sides set the machine's record
   `secrets_account` and `secrets_write` (the target its own record, since peers can't change a
   machine's own record),
   and the run ends with a sync.
5. Verify with `_secret-probe --verify`: `op user get --me --format json` with the stored token
   must report a `SERVICE_ACCOUNT`. One line per machine: `ok`, `skipped` or `failed` with a fix.
   A failure after an account exists says to revoke it and rerun.
6. If this machine wasn't chosen, has the 1Password app and no provider yet, it is set to
   1password desktop with the vault; if it is already desktop with no vault, it gets the vault.
   The run ends with where to revoke (https://start.1password.com/ > Developer > Service accounts;
   `op` has no revoke command) and how to store secrets (`yonder secret set NAME`).

Exit 1 if any machine failed; skipped machines don't fail the run. Every machine whose agents use
`--secret` needs its own token and `op`, because secrets resolve on the caller.
`secret ls --json` is `{"provider":"1password","mode":"desktop","vault":"...","vault_error":"",
"extra_vaults":[{"vault":"...","error":""}],
"secrets":[{"name":"...","ref":"...","source":"secrets.toml","vault":""},{"name":"...","ref":"","source":"vault","vault":"..."}]}`.
`vault` is empty when none is recorded; `vault_error` is set when listing it failed, and each
`extra_vaults` entry's `error` likewise. A vault item's `vault` names the vault it resolves from.

`init` runs the same setup as `yonder secret setup`, but leaves an existing provider alone. It
recommends desktop mode where the 1Password app is installed and the session is not headless, and
service-account mode otherwise. With `--yes` it only prints the non-interactive commands.

`doctor` gets `secrets.Health()`: provider configured, CLI installed and its version, for desktop
whether `op` can see the app (`op whoami`, then `op account list`; no item is read), for hands-free
modes whether a token is stored (and for 1Password, whether `op whoami` accepts it, naming the
service account when provisioning recorded it), for 1Password the agent vault and its access
("read-only" or "read and write"; desktop mode is read and write) and the extra vaults it also
reads, and whether each reference is valid for this machine's provider. In service-account mode
each extra vault gets its own check, `op vault get <V> --format json` (metadata, no item read),
which fails with the `secret setup ... --extra-vault ... --replace` line that grants it; desktop
mode only lists them, since checking would ask for an approval. Every failure carries a fix.

## Agent skill

`yonder agents install` copies `skill/yonder/` (embedded with `go:embed` from `skill/embed.go`)
into each harness's user skills folder: `~/.claude/skills/` (Claude Code; it doesn't read
`~/.agents/skills`), `~/.agents/skills/` (Codex, Pi), `~/.hermes/skills/` (Hermes). It writes only
its own `yonder/` folder and never edits a harness's settings file. (Syncing the rest of a
harness's config is the agents store, below.)

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

## Store sync

A store is a folder under `~/.local/share/yonder/<store>` that syncs peer to peer over the same
pinned ssh transport as everything else. There is no git remote and no daemon. Data stays on
the tailnet. A user who wants git history can `git init` inside a store; yonder ignores `.git/`
and never runs git. The agents store is the only one so far; another plugs into the same engine. A
store registers a kind: its name, its layer, which paths it ignores, a check every file must pass
on both sides, which incoming changes wait for review, and a hook that runs after a sync changes
files there.

**Versions.** The state file (`sync/<store>.json`) holds, per path, the sha256 of its content, the
exec bit, the size, a version counter per machine, a deleted flag, and its origin: machine, time
(the file's mtime for an edit found by a scan, never more than 5 minutes ahead), and harness when
yonder made the change itself (`import`, `restore`, `resolve`, `accept`; a scan doesn't know who
edited a file, so it records no harness). A scan bumps this machine's counter for every file whose
hash or exec bit changed since the last scan and for every file that went missing (a delete
marker). That is how edits made without yonder are caught without a watcher. A delete marker is
dropped 90 days after its origin time, on every machine alike, so markers expire even in a fleet
that syncs often. A marker dated more than 5 minutes ahead of the receiving machine's clock is
taken as dated now, so it expires too.

The scan reads the store folder through a symlink at its own path (a store moved into a dotfiles
repo and linked back is the same store). It stops, changing nothing, when the folder is missing
while files are on record, or when it would delete two or more files that are more than half of
what is tracked (so 2 of 3 and 8 of 9 stop; one file gone from a tiny store, or 4 of 9, is just a
delete): a moved or broken folder looks like that, and the deletes would spread to every machine.
The error says what it found; `yonder sync --allow-deletes` goes ahead. `doctor` reports a held
store as a FAIL with that fix. A peer whose scan stops this way exits 5, and the caller prints
`agents: gpu-box held: mass delete on gpu-box (8 of its 9 files are gone)` with the fix to run
there; it doesn't fail the caller's sync.

**Pairs that haven't met for a long time.** Delete markers are dropped after 90 days, so two
machines that haven't synced with each other for longer could bring back files one of them deleted
meanwhile. The rule is per pair. Each machine records its last sync with each other machine
(`peers` in the state; a receiver records one when the caller pushes to it), and an offer carries
the offering machine's record of its last sync with the caller (`last_with`). The pair's last
sync is the later of the two. When it is more than 90 days ago, a file that only one side has,
unchanged since that sync (its origin time is no later), is held back: not sent, not taken, and
reported (`held back: ...`). A file both sides know syncs as usual, delete markers included, and
so does anything changed since the pair last met, since a delete can't have been aimed at it. At
a first meeting the pair has no last sync, so each side's last sync with anyone stands in (the
offer carries the peer's as `last_any`), and the earlier one counts: a machine back from a long
absence meeting one that joined meanwhile still holds its old files back. A brand-new machine
has synced with no one, so the other side's time decides, and it gets everything a fresh
machine has. Delete markers for paths a side has never held are passed on too, quietly: a
machine that joins takes the markers it is offered, so it turns away a deleted file from a
machine that hasn't heard, while the marker lasts.

When a sync holds files back, it prints one line for that machine instead of one per file, also
from `yonder add`:

```
agents: held back 3 files with newbox, since one of you last synced more than 90 days ago and the other may have deleted them meanwhile; if the old machine's deletes are already reflected, run `yonder sync --stale-ok`
```

So a new machine added while another machine has been off for over 90 days gets only the files
changed since then, until that is run.

A hold outlives the sync that found it, since that sync makes the pair fresh again (and the
machine's last sync with anyone with it). The machine that has the file records it
(`held_files`: path, since when), whether it was the caller or the caller told it to (`hold` in
the push bundle). Such a file may be one the fleet deleted and forgot, so it is held from every
machine, not only the one that found it: its offers flag the file `held`, and any peer that
doesn't know the path skips it. The flag exists only on the wire: the caller reads it
and clears it from every state it takes from an offer, and a bundle's reader drops it, so it is
never saved or passed on. A hold ends when the file changes after it, when the peer turns out to
know the path, or with `yonder sync --stale-ok`, which syncs held files and clears every hold
the machine keeps. `doctor` warns for each machine last synced more than 90 days
ago.

**A sync with one peer** (the caller holds its store lock; the peer takes its own, waiting up to 5
seconds, else it exits 4 and the caller skips it with a note, which doesn't fail the sync):

1. The caller scans. `_store-offer <store>` makes the peer scan and print its manifest:
   `{"v":1,"store","machine","files":{path: state},"refused":[...]}`.
2. Per path, the counters decide. One side has seen everything the other has: it wins and its
   version moves. Equal counters: nothing. Neither (concurrent): same content merges the counters;
   a delete against an edit keeps the edit (doctor warns for 7 days); two edits keep both: the
   later edit time stays at the path (a tie goes to the machine name that sorts first), the other
   is written beside it as `<name>.conflict-<machine>-<yyyymmdd>.<ext>` (a dotfile keeps its whole
   name as the stem; `-2`, `-3` when taken). Both new versions get counters that beat both sides,
   so every machine converges. Nothing stops syncing. If the copy can't be written (its name is
   refused, or too long), neither side changes that path and the sync says so, every time, until
   one edit is undone by hand: replacing the loser without its copy would lose it.
3. `_store-send <store>` with `{"paths":[...]}` returns what the caller needs as a bundle. A path
   whose counters changed since the offer is left for the next sync.
4. The caller applies the changes here, then `_store-recv <store>` applies the rest there.

A bundle is a tar stream: a `.yonder-store.json` entry first (`{"v","store","from","files"}`),
then one regular-file entry per path that needs content. The reader takes the whole stream
before anything is applied, refuses an entry the metadata doesn't list, a non-regular entry, a
path that isn't clean and relative, a file over 1 MiB, more than 50 MiB in all, or content that
doesn't match its hash. Applying a version requires that it beats what is here, and that the file
on disk is still what the state says (an edit since the last scan is left for the next sync). The
receiver also checks the store's own limits (50 MiB and 10,000 files in all, counting what it
holds), refuses a path that differs from one it holds only by case (a Mac stores them as one
file), and runs the store's check. A version the sender marked allowed is recorded as allowed here
too, so this machine's own scans take it. Files
are written through an `os.Root` on the store folder with a temp file and a rename, so no link in
the store can send a write elsewhere.

`yonder sync [store] [machine...]` syncs the registry, applies the layers, then each store whose
layer is on with every online, set-up peer (or the named ones), in order, then once more with any
peer synced before this machine received something, so one run relays changes between peers.
Output is one line per peer and store (`agents: gpu-box: get 1, send 2`, or `up to date`), then a
line per path received (with its origin), sent, waiting for review there, in conflict, or skipped,
up to 20. `--dry-run` plans and prints without changing anything here (the peers still record their
own edits when they scan). `--background` starts `yonder sync` in its own session with output in
`sync/last.log` and returns at once, for a session-start hook; `YONDER_SYNC_BACKGROUND=off` turns
those starts off. `--resolve <path> --keep mine|theirs|<machine>` settles a conflict first: the
chosen version goes at the path (`mine` is this machine's, `theirs` the only other one), every
conflict copy is deleted, and the sync spreads it. `refresh`, `add` and `enable` sync the stores
too, printing only what moved and what failed. Commands that change the agents store here
(`import`, `restore`, `allow`) start a background sync.

Two machines that are never awake at the same time only meet through a third machine, or a timer.
A cron or launchd entry running `yonder sync` on an always-on machine is that timer; it is a
timer, not a daemon.

**Refusals are loud.** A file a store won't sync is a FAIL line in `sync` output (exit 1) and in
`doctor`, with the reason and the fix. Only regular files sync: a symlink, a folder entry yonder
can't read, a path with an empty, `.` or `..` part, a file over 1 MiB, or one that would take the
store over 50 MiB or 10,000 files is refused, as is a path that differs from another only by
case. `.git/`, `.DS_Store` and yonder's own `.yonder-*` temp files are
ignored. A refusal the peer reports, or a file the receiver refuses, is a FAIL too.

**History.** Before a sync, resolve, restore, accept or import replaces or deletes a file, the old
content goes to `sync/history/<store>/<path>/<unix-nanos>.data` with a `.json` beside it (its state
and why it was kept). The last 10 per path are kept, each for 30 days. Edits made outside yonder
are not kept until something replaces them.

**Review.** A store marks some incoming changes for review. Such a change from another machine is
not made live: the store's version of the path goes to `sync/pending/<store>/<path>`, the live
file stays, and the state records what is live. It still syncs on to other machines as the
store's version. A change made here, a delete, and content that is already live never wait. An
edit here to a path that waits replaces the waiting change (which goes to history). Whether a
change was made here is decided from this machine's own store, never from a version's origin,
which the sender writes; and content that waits in pending/ came from elsewhere, so a waiting
version that wins a conflict or beats a delete in a sync this machine runs still waits. A restore (and a resolve that picks a waiting copy) of a path the store
reviews goes to pending/ too, since history also holds changes that were never accepted.

## Agents store

The agents layer syncs `~/.local/share/yonder/agents`:

```
briefing.md                   the hand-written part of the briefing (see The briefing)
render.toml                   each harness's name for each model tier (see Model names per harness), and [shared] opt-ins
instructions.md               shared, harness-neutral
instructions.<key>.md         an optional overlay for one harness: claude, codex, pi or hermes
skills/<name>/...             one folder per skill
claude/agents/*.md            Claude Code subagents
claude/commands/*.md          Claude Code commands
hooks/*                       hook scripts (Claude Code)
files/<key>/<name>            a file linked as is into that harness's folder (hooks.json, a model catalog)
machines/<name>/...           the same layout, applied only on that machine
```

The overlay key for Claude Code is `claude`, to match its own folder name. A machine's overlay wins
over the shared one by name. A machine's own map (`~/CLAUDE.md`) stays out of the store.

### What never syncs

Every rule below matches paths with case folded, since a Mac stores `Hooks/` and `hooks/` as one
folder. A Mac also folds some non-ASCII letters into ASCII ones (`hookſ/` into `hooks/`, `ﬁles/`
into `files/`), so the path segments the rules look at must be plain ASCII: the top folder, a
`machines/` name, a skill or `claude/` folder name, and a `files/` harness and file name. A path
with a non-ASCII character there never syncs. Deeper names, such as a file inside a skill, may be
anything. A file name that looks like a secret never syncs, on the sending or the receiving side:
`.env*`, `*.pem`, `*.key`, `*.p12`, `*.pfx`, `id_*`, `auth.json`, `credentials*`, `.credentials*`,
`.git-credentials`, `.netrc`, `_netrc`, `*.age`, `*token*`, `*secret*`. `skills/yonder/` (each
machine installs its own) and `skills/synced/` (Claude.ai's own folder) never sync. Under
`files/<key>/`, a harness's own settings, auth and instruction files never sync and are never
linked: `settings*.json`, `config.toml`, `config.yaml`, `config.yml`, `.claude.json`, `auth.json`,
credentials, `CLAUDE.md`, `CLAUDE.local.md`, `AGENTS.md`, `SOUL.md` (instructions go in
`instructions.md`). An instructions file holding yonder's own marker (`<!-- yonder:`) never
syncs and is never rendered, since it would break the block it lands in. Content that looks like a key is refused with its line number:
private key blocks, `sk-...` API keys, GitHub, Slack and AWS key formats, and long runs that mix
case and digits with near-random entropy (hex hashes don't count). Lines holding `op://` or
`bws://` references skip the entropy check, since references and secret names are fine. This
catches common formats, not everything. `yonder agents allow <path>` waives the content check for
that file's current content (the state carries it, so receivers take it too); a later change is
checked again. A refusal by name (and the marker one) can't be waived. Yonder itself never copies a harness's settings,
auth or credential files into the store: `import` takes only the kinds below. An instructions file
or `briefing.md` that names a path under this machine's own home (`$HOME/...`) is a doctor WARN,
since that path breaks where the home differs; `~/` works everywhere. Another machine's home, or a
path written as `<machine>:/path`, is fine: the briefing names those on purpose.

### Review

Hooks run code, so a new or changed hook from another machine waits for `yonder agents accept`
before it is live here. Executable files count too, and so does everything under `files/`: a file
linked into a harness's own folder (a hook or MCP config, a model catalog) can change what runs. The
setting is `[sync] review` in `config.toml`: `"executables"` (default), `"all"` (every incoming
change waits) or `"none"`. `accept [path...]` shows each waiting change as a line diff against
what is live (a change to the exec bit as its own line; control, format and bidi characters
shown escaped) and asks; it needs a terminal on stdin and stdout and refuses otherwise, so an agent
can't accept on its own. `agents status`, `diff` and `doctor` show what waits.

### Wiring the harnesses

`yonder agents link [--harness h]... [--dry-run] [--replace-links]` wires the store into each
harness found here (the same detection as the skill install), or the ones named:

| Harness | Instructions | Skills | Other |
|---|---|---|---|
| Claude Code | A marked block in `~/.claude/CLAUDE.md` holding the briefing, then the text of `instructions.md`, `instructions.claude.md` and the machine's two, where they exist, joined with blank lines. The user's own text stays. | One link per skill in `~/.claude/skills/<name>`. | One link per file in `~/.claude/agents/`, `~/.claude/commands/` and `~/.claude/hooks/`; `files/claude/*` into `~/.claude/`. |
| Codex | `~/.codex/AGENTS.md` rendered from the same files with the codex overlay, when missing or yonder's; else the rendered text in a marked block. | One link per skill in `~/.agents/skills/<name>`. | `files/codex/*` linked into `~/.codex/`. |
| Pi | Same as Codex, into `~/.pi/agent/AGENTS.md`. | `~/.agents/skills` (shared with Codex). | `files/pi/*` into `~/.pi/agent/`. |
| Hermes | A marked block in `~/.hermes/SOUL.md`, only when that file exists, holding the briefing and `instructions.hermes.md` (and the machine's), not the shared `instructions.md`. Hermes is a messaging gateway with its own persona, so coding-agent rules stay out of it unless `[shared] hermes = true` in `agents/render.toml` (then the shared text and the machine's come after the briefing, as for the others). Hermes loads SOUL.md from its home in every session, whatever the working folder; its AGENTS.md, CLAUDE.md and `.hermes.md` come from the working folder only, so none of those is global. It seeds a default SOUL.md when there is none, so yonder never creates it; link says to run Hermes once first. Its fleet lines outside the block are the user's to drop; doctor flags any that repeat the briefing. | Not wired: link prints that the store's skills folder can be added to `skills.external_dirs` in its config. | `files/hermes/*` into `~/.hermes/`. |

The text is copied in, never `@`-imported. Claude Code asks once per project before it imports a
file outside the project, and keeps the import off if declined; a headless session can't answer,
so an import from the user-level file would be unreliable. Codex and Pi have no imports anyway.
The copy means a store edit reaches a harness only when it is rendered again (below); doctor
warns while a rendered file is behind the store. There is no hard size limit, but every session
loads the whole text, so doctor warns when one harness's rendered instructions pass 25 KB.

The rules:

- Yonder never edits a settings file (`settings.json`, `config.toml`, `config.yaml`). Claude Code
  hooks are linked into `~/.claude/hooks/`, so a `settings.json` entry for `~/.claude/hooks/x.sh`
  keeps working; registering a new hook stays the user's step. So does a session-start hook
  running `yonder sync --background`, which link suggests.
- It edits a user's instructions file only inside the marked block: from the first line that is a
  begin marker (`<!-- yonder:begin ... -->`) to the first line after it that is
  `<!-- yonder:end -->`. Markers count only as whole lines, so text that mentions them (in
  backticks, say) is the user's, before or after the block. A begin marker with no end is
  refused and the file left alone; the error and `doctor` say to delete the begin line or add the
  end. A rendered file starts with a marker line. Whether a file is yonder's is decided from that
  line every time, never from what link wrote before: a rendered file the user rewrote is theirs
  and gets a block. Before changing a file that isn't exactly what yonder last wrote (the entry
  keeps its hash), link copies it to `agents/backup/<ts>/`, every time.
- Anything already at a link's path is moved to `agents/backup/<ts>/<path under ~>` first, never
  beside it (a folder there would be loaded as a skill). Only a symlink to the recorded store
  target counts as link's own; a real file at a linked path (an editor's save by rename) is backed
  up too.
- It never writes through a symlink. An instructions file, or a folder on the way (such as
  `~/.claude/skills`), that is a symlink is refused, naming `--replace-links`. With it, the link is
  replaced and its old target recorded, and what it gave the harness comes along: a file gets the
  target's text with the block added; a folder becomes a real folder holding one link per entry
  of the target (a name the store also has goes to the store, with a warning), plus the yonder
  skill if the target had none. Unlink takes those out and puts the symlink back; a file whose
  text changed meanwhile is backed up first.
- `agents/links.lock` serializes link and unlink on one machine, so a relink after a sync and a
  layer change arriving through `_sync` can't edit `links.json` at once. A relink checks whether
  links are paused under that lock, so one that started before a layer-off unlink does nothing.
- Conflict copies, dotfiles, pending changes and names that never sync are not linked. Link is idempotent; a second run
  with nothing new changes nothing. Entries no longer wanted (a skill deleted from the store) are
  undone, putting back what they replaced.
- After a sync changes the store on a linked machine (either side), link runs again, so new skills
  get links and rendered text is rendered again. That includes edits made here: the caller runs it
  after its own scan finds changes. Links point into the store, so edits to linked files need
  nothing more.

`links.json` lists every entry: path, kind (`link`, `block`, `render`, `dir`), the store target,
the backup, the old symlink target, whether yonder created the file and the hash of what it last
wrote. `yonder agents unlink` undoes every entry and puts back what it replaced: backups move back,
replaced symlinks come back, blocks come out (a file yonder created and that holds only the block
is removed). It removes only what is recognisably its own: a symlink to the recorded target, a file
starting with its marker (backed up first if changed since yonder wrote it), the text between its
markers. A file without the marker is left alone and named. It always
restores, so there is no `--restore` flag: removing the links without putting the originals back
would leave a harness with neither. `doctor` warns for an entry that no longer matches
(a link changed, a block deleted by hand, rendered text behind the store).

### The briefing

Every agent should know the user's machines without asking. The briefing is that: a short
operational map, rendered in full as the first part of yonder's block in every harness's
instructions file. It is not memory. Personal and project facts stay in each harness's own
memory; the briefing holds only facts about the machines and how to use them.

It has two parts. The machine list is generated from the records and never hand-written: per
machine its name, role, address, OS and architecture, CPU, GPU and RAM, the models a server
answered for at discovery, and one availability word from the role and notes (`always-on`,
`sleeps`, `intermittent`, `travels`). This machine is marked. The rest is `agents/briefing.md`,
hand-written: routing (which work goes where), where things live, how secrets are used by name.
Online state is left out on purpose; `yonder ls` answers it live.

```
## Yonder briefing

Machines (generated from yonder's records; live state: `yonder ls`, details: `yonder show <machine>`):
- `gpu-box` gpu · 100.64.0.2 · Ubuntu 24.04.1 LTS amd64 · AMD Ryzen 9 7950X, RTX 4090 24 GB, 64 GB RAM · serves llama3:8b (ollama :11434) · sleeps
- `laptop` laptop, this machine · 100.64.0.1 · macOS 15.1 arm64 · M3 Pro, 36 GB RAM · travels
All reached as user `me` over Tailscale. Run with `yonder run <machine> -- <cmd>`, ...

## Routing
- Heavy jobs go to `gpu-box`; models are in `gpu-box:~/models`.
```

In `briefing.md`, a `reviewed: YYYY-MM-DD` line under a heading dates that section; one before
the first heading dates the whole file. Those lines don't render. Paths are written as
`` `<machine>:<path>` `` for one machine or `` `~/...` `` for every machine, so they can be checked.

**Rendering.** `agents link` renders it, and so does every relink: after a sync changes the store,
and after every registry sync on either side (`ApplyLayer` relinks where linked), since a role,
note or fact change alters the list. A store with no `briefing.md` still gets the list.

**Budget.** Every session loads all of it. The rendered briefing has a hard cap of 4 KB with a
warning at 3 KB. The shares: the hand-written part has its own cap of 2 KB, and the machine list
gets the rest of the 4 KB, so at least 2 KB. The list grows with the fleet and with served models,
without anyone editing anything, so it steps down to fit its share rather than fail: first each
machine's served models become a count ("serves 4 models"), then the OS and hardware go, then
each line is only name, role and address. A shortened list says so and points at `yonder ls` and
`yonder show <machine>`; `agents status` and `doctor` add "machine list shortened to fit".

Over a cap nothing is cut into the briefing. A `briefing.md` over 2 KB is refused by the store
check, so it doesn't sync (no `allow` waives it). When the briefing can't render (the hand-written
part over its cap, a list too long even at its shortest, a bad record, a tier with no name in
`briefing.md`), the instructions render without it, so one bad briefing never freezes the rest
of the text on every machine. The failure stays loud: link returns an error naming each file
that has no briefing, the size, the cap and the three largest sections; `yonder sync` prints
`FAIL agents: <file> has no briefing: ...` and exits 1; `doctor` FAILs. A problem in the
instruction files themselves (a tier with no name there) still leaves that file as it was.
`agents status` and `doctor` show the usage, like `briefing 2.0 KB / 4.0 KB (warn at 3.0 KB),
hand-written 1.2 KB / 2.0 KB`.

**Records are cleaned.** The list is built from records that arrive from other machines through
`_sync`, so every field is cleaned at render time: one line, no control, escape, format (bidi,
zero-width) or line-separator characters, no backticks, single spaces, clipped (OS 40, CPU and
GPU 40, a model 60, a runtime 16 characters). The role is used only when it is one of `laptop`,
`desktop`, `server`, `gpu`, `other`; the ssh user only when it is a plain user name; the address
only when it parses as an IP; a machine whose name isn't valid is left out. A list that still
holds yonder's marker (`<!-- yonder:`) or a `{{model:` placeholder is refused, so the briefing
is left out until the record is fixed (`yonder refresh <machine>`).

**Upkeep.** Nobody has to remember to keep it true. `doctor` WARNs, and `agents briefing review`
lists:

- a path that doesn't exist on its machine: `<machine>:<path>` there, `~/...` on every set-up
  machine. Each machine is checked with one read-only `_exec` (`[ -e ]` only; this machine with a
  stat). A path is cut before its first placeholder segment (`~/repos/<repo>` checks `~/repos`).
  An offline machine is "unchecked", not a failure.
- a machine name that isn't in the registry, or that was removed: in `<machine>:` paths, in
  `yonder run|show|jobs|set|refresh|mesh <machine>` (not `add`, whose machine is new, or `sync`, whose first word may be a store), and a removed machine's name in
  backticks.
- text that contradicts the records: an address no machine has or that belongs to another
  machine on the line, a role claimed for a machine ("`gpu-box` is the server", "`gpu-box`
  (server)", "the server, gpu-box") that differs from its record, and an address or a whole line
  that repeats the generated list.
- a section with no `reviewed:` date, or one older than 90 days.
- a line of the briefing that the user's own text repeats outside the block in the same
  instructions file. The user's copy can go; the briefing loads there anyway.

`review` then prints a suggested draft of `briefing.md` as a diff: the repeated addresses and
lines taken out. The rest needs a person or an agent with one, so it stays a finding. Yonder never
writes the draft; `--draft` prints it for an agent or the user to edit.

### Model names per harness

Rules that name models don't fit every harness: Claude Code's tiers and Codex's are named
differently, and a find-and-replace would also change the same words in prose. So the store's
text names a tier, `{{model:<tier>}}`, and each harness gets its own name for it from
`agents/render.toml`, which syncs like any store file:

```toml
[claude]
top = "Atlas"
fast = "Pebble"

[codex]
top = "Zephyr"
fast = "Quill"
```

Only that exact syntax is replaced (`{{model:` then a tier of a-z, 0-9 and `-`, then `}}`); `{{
model:top }}`, `{{model:Top}}` and the word "top" stay as they are. It works in
`instructions*.md`, machine overlays included, and in `briefing.md`. The tables are `[claude]`,
`[codex]`, `[pi]` and `[hermes]` (the overlay keys); each value is one short line, at most 100
bytes, with no control, escape, format or line-separator characters, no `{{` and no marker. A
broken table matters only to text that uses a placeholder. One more table,
`[shared]`, holds opt-ins: `hermes = true` gives Hermes the shared instructions too (see Wiring
the harnesses); it takes only `hermes` and a true or false. A table that breaks these rules is
refused by the store check, so it doesn't sync.

A tier a harness has no name for, whether no table defines it or only other harnesses' do,
refuses that harness's render: its file keeps what it had, and the error names every missing tier,
the files that use them and the line to add (`add fast = "..." under [codex] in
agents/render.toml`). Other harnesses still render. `agents status` (`unresolved`, like `"codex:
deep, fast"`) and `doctor` (a WARN each) list the missing ones for the harnesses linked here and
every harness with a table.

When to use which: a placeholder when a rule is the same for every harness and only the model's
name differs ("plan with {{model:top}}, scout with {{model:fast}}"). An overlay,
`instructions.<key>.md`, when the rule itself differs: a harness that has no subagents, a
setting only one of them has, a step that works differently there.

### Importing

`yonder agents import <path> [--kind k]... [--harness h] [--machine m] [--replace] [--dry-run]`
copies config into the store. Kinds: `instructions`, `skills`, `subagents`, `commands`, `hooks`,
`files`; nothing else goes in. A folder is read as a harness or config folder: the first of
`instructions.md`, `CONTEXT.md`, `AGENTS.md`, `CLAUDE.md` becomes `instructions.md`, and `skills/`,
`agents/`, `commands/` and `hooks/` map onto the layout; `--kind` narrows it. A folder with a
`SKILL.md` is one skill. A single file needs `--kind` unless its name is one of the instruction
names. `--harness` turns instructions into that harness's overlay and is required for `files`;
`--machine` puts everything under `machines/<m>/`. Symlinks in the source are followed to regular
files; `.git/`, dotfiles in kind folders, `skills/yonder`, `skills/synced` and the harness
settings names above are skipped with a note. An instructions file that holds yonder's block
(a linked `CLAUDE.md`) gives only the text outside it; a file yonder rendered is skipped. A store file that differs is left alone unless `--replace` (the old one goes to history).
Every file passes the secret check first; a refused one is a FAIL line and exit 1.

### Machines whose instructions differ

Before the agents layer, each machine has its own `~/.claude/CLAUDE.md` and `AGENTS.md`, and
they drift apart. Yonder never merges prose or picks one: an agent does it with the user.

`yonder agents gather [--json]` reads each online machine's own instructions files
(`~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, `~/.pi/agent/AGENTS.md`; Hermes's `SOUL.md` is its
persona and is left out). This machine's are read directly; every other machine's through one
read-only `_exec` that prints each file that exists (files over 256 KB are skipped). Yonder's
block is taken out and a rendered file counts as empty, so only the user's own text is
compared. Each file is split into sections: a heading and what follows it, up to the next
heading of any level; text with no heading above it splits into paragraphs. Sections match by
heading (lower case, a second `## Notes` in one file is `## notes #2`), paragraphs by their
text. Each section is then:

- `same`: one version, on every machine that has instructions.
- `differs`: two or more versions, between machines or between one machine's files.
- `some`: one version, on more than one machine but not all.
- `only`: on one machine only.

`--json`: `{"machines":[{"name","files":[{"path","harness","bytes","yonder","rendered"}],"error"}],
"sections":[{"key","heading","status","machines","versions":[{"machines","files","text"}]}],
"summary":{"same","differs","some","only"}}`. A machine offline or not set up is listed with an
`error` and no files.

The yonder skill has the reconcile procedure for the agent: sort each section into shared,
machine-only or conflicting; ask the user about each conflict; show the result as a diff; after
approval, write `instructions.md` and `machines/<m>/instructions.md` with `agents import` (no new
apply step is needed: `import --kind instructions --machine <m>` writes an overlay); then trim
what moved out of each machine's own file, backing each up first.

When `enable agents`, or `init` with the layer on and machines added, finds that two or more
machines have instructions that don't all agree, it says so and names the procedure. It writes
nothing.

### The other commands

- `agents log [path] [-n N] [--json]`: the changes this machine recorded, oldest first: time,
  action (`edit`, `delete`, `pending`, `accept`), path, the origin machine and harness, and the
  machine it arrived through.
- `agents diff <path> [--version N]`: a line diff between the file now and the version it
  replaced (or kept version N); for a waiting change, between live and incoming.
- `agents restore <path> [--version N | --list]`: put a kept version back; it syncs like an edit.
  For a reviewed path it waits for `accept` here first.
- `agents briefing show [--harness h]` prints the briefing as that harness gets it, with its size
  on stderr. `agents briefing review [--json] [--draft]` prints the findings and a suggested draft
  (see The briefing).
- `agents status [--json]` adds `"store"` to the skill report: `layer_on`, `dir`, `files`, `bytes`,
  `review`, `linked`, `paused`, `pending`, `conflicts`, `refused`, `last_sync` (per machine),
  `notices` (deletes that lost to an edit, 7 days), `home_paths`, `link_problems`,
  `large_instructions` (rendered instructions over 25 KB), `stale_peers`, `held` (why the next
  sync would stop here) and `briefing` (`bytes`, `warn`, `cap`, `hand_bytes`, `hand_cap`, and
  `error` when it can't render) and `unresolved` (see Model names per harness).

`import`, `link`, `accept`, `restore` and `allow` need the layer on; `log`, `diff`, `status` and
`unlink` work either way.

## Doctor

`yonder doctor [--json]` prints one line per check, grouped by machine: PASS, WARN or FAIL, a
one-line detail, and a fix under anything not passing. It exits 1 if any check failed, else 0.

- This machine: tailscale installed, running, logged in; config and self record (and whether a
  tombstone says it was removed); sshd host key; stale facts; log dir writable; the skill for each
  detected harness; `authorized_keys` free of lost machines' keys (a key the lost machine shares
  with a live record is its own FAIL, naming those machines, with fix "make new keys there, then
  `yonder remove <lost> --lost --include-shared --yes`"; when `authorized_keys` or `~/.ssh` is a
  symlink the check reads through it and the fix says to delete the lines by hand);
  `secrets.Health()` when the secrets layer is on (a single PASS saying it is off otherwise); a
  `layers` line. Mesh (below) for this machine's own file.
- The agents store, when its layer is on: a PASS with the file count and the last sync per
  machine; a FAIL per file that can't sync; a WARN per conflict (fix `yonder sync --resolve <path>
  --keep mine|theirs|<machine>`), per change waiting for review (fix `yonder agents accept`), per
  delete that lost to an edit in the last 7 days, per instructions file naming a path under this
  machine's home, per link entry that no longer matches or block with no end marker (fix `yonder agents
  link`), per harness whose rendered instructions are over 25 KB, and per machine last synced
  more than 90 days ago (files only one side has are held back); a `briefing` line with its usage
  (WARN past 3 KB, FAIL over a cap) and a WARN per briefing finding (see The briefing); a WARN per
  harness with a `{{model:...}}` tier it has no name for; a FAIL when the next sync would
  stop here: the store folder is missing while files are on record, or a mass delete (fix: put
  them back, or `yonder sync --allow-deletes`).
- Each fleet machine, in parallel: in tailscale status; online (offline is a WARN with last seen
  and notes, and its other checks are skipped); pending (WARN; with no held pin the detail is
  "<m> is online now; run `yonder add <m>` to finish setup", with a pin the fix is `yonder refresh <m>`); no pinned host key (fix `yonder add
  <m>`); record address matches the tailnet (FAIL, fix `yonder remove <m> && yonder add <m>`); ssh with the pinned host key, by running
  `yonder version` there (BatchMode, 5s connect timeout); same yonder version as here; stale
  record (`discovered_at` older than 7 days, fix `yonder refresh <m>`); skill state from
  `yonder agents status --json`; its `authorized_keys` for lost machines' keys and for mesh.
- Mesh, for each machine whose `authorized_keys` it reads (this one, and each online set-up
  peer): WARN when a fleet machine that mesh would hand keys out for has none of its `mesh_keys`
  in that file, "laptop can't ssh in: none of its keys on record are in ~/.ssh/authorized_keys",
  fix `yonder mesh <m>`. A machine with no `mesh_keys` on record can't be checked and is left out.
  It checks which keys a file trusts, not which key a machine's ssh actually offers. WARN for each
  removed machine that still has lines tagged for it (the same count `remove` would strip), fix
  `yonder remove <name> --yes`. WARN for other tagged lines mesh would prune (outside the grant
  set), naming whose, fix `yonder mesh`.
- Registry consistency: each online machine's `_sync` reply to an empty bundle (it merges nothing
  and returns what that machine has) must name the same machines as this one.

`--json`: `{"self","version","ok","summary":{"pass","warn","fail"},"checks":[{"group","name",
"status","detail","fix"}]}`.

## Removing a machine

`yonder remove <m> [--lost [--include-shared]] [--yes]` writes a tombstone, deletes the record here and pushes the
tombstone to every online peer with the usual sync. It asks first unless `--yes`, and with no
terminal it needs `--yes`. It refuses to remove `self`. The removal is logged here as a
caller-side entry (`"command":"yonder remove <m>[ --lost]"`).

- Default: if `m` is online, yonder first runs `yonder agents uninstall && yonder remove
  --forget-local` there through `_exec`. `--forget-local` clears that machine's `machines/`,
  `removed/` and `self`, and keeps the rest of `config.toml`. The binary stays; the output says
  how to delete it, and names its 1Password service account to revoke when the record has one. If `m` is offline, the output gives that line to run there later. A pending
  `m` (never set up) only prints `removed <m> (it was never set up)`, plus the tombstone sync.
- `--lost`: `m` is never contacted. On this machine and every online peer (`yonder remove
  --strip-keys --for <m>` through `_exec`, keys on stdin, before the sync) yonder removes the `authorized_keys` lines whose
  key type and base64 match one of `m`'s `ssh_client_keys`. Options prefixes and comments don't
  matter for the match, and other lines are kept byte for byte. The original is copied to
  `authorized_keys.yonder-bak.<unix-nanos>` first, a new backup per change, so a later strip
  never overwrites an earlier backup; a missing file is fine.
  A key that a live record (self included) also lists is shared: `add` records every
  `~/.ssh/*.pub`, so a copied key or one used on several machines shows up on more than one
  record, and stripping it cuts those machines off too. The plan lists the keys only `m` uses
  apart from the keys `m` shares, each with the machines that share it. Shared keys are kept
  unless `--include-shared` is given. The same rule runs on each target: `--strip-keys --for <m>`
  keeps a key any of its own records other than `m` lists, unless `--include-shared` is passed
  along. If `~/.ssh/authorized_keys` or `~/.ssh` is a symlink, yonder doesn't rewrite it: it
  reports the real path and the exact lines to delete by hand, and that machine counts as still
  trusting the key. Then it prints a checklist: delete
  `m` from the tailnet, revoke its secrets token (naming its `secrets_account` when known), rotate what it could read, remove its keys from
  other services, name any shared keys it kept with the machines to make new keys on, and note
  which machines it couldn't reach. Those strip the keys themselves when
  a later sync reaches them (see Sync above), shared keys included when the tombstone has
  `include_shared`. Running `remove --lost` again works from the
  tombstone alone and always strips, applied or not; a rerun with `--include-shared` also sets
  `include_shared` on the tombstone (same `removed_at`), so machines offline now strip the shared
  keys at their next sync; `doctor` fails while any machine still
  trusts a lost key.
- Every remove (plain, `--lost`, and reruns) also deletes the lines `yonder mesh` added for `m`
  (tagged `yonder:<m>`), here and on every online machine, with the same read-edit-write as mesh
  (backup first, symlink refused, nothing else touched). A key a live record also lists is kept,
  since mesh tagged a shared key with whichever machine came first, unless `--lost
  --include-shared`. Lines the user added by hand are left alone, so plain remove still says the
  machine's other keys are trusted. A machine offline now drops the lines at its next sync: after
  merging, `_sync` (and the caller after every sync) strips the tagged lines of every machine it
  holds a tombstone for, under the same keep rule (`include_shared` from the tombstone). That
  needs no applied-list: a run with nothing to strip doesn't write. `yonder remove <m>` on an
  already removed `m` reruns only this cleanup on the machines online now and exits 0.
- Offline peers keep the record until a sync reaches them; any later `yonder refresh` from a
  machine that has the tombstone pushes it, and a lost tombstone strips the keys there too. A removed machine that comes back and syncs is refused
  by every machine holding its tombstone (see Trust model).
- The tombstone is dated now. If the record's `added_at` is in the future (clock skew), it is
  dated one second after it instead, but never more than 4 minutes ahead, so peers' skew guard
  still takes it. The record is dropped here either way.

## Trust model

Yonder assumes a single-owner tailnet: one person's machines, with no nodes shared in from
another tailnet. `add` and `init` enforce the part they can: a peer another account owns needs
`--allow-shared`, and a short name two peers share needs the full DNS name. Agents are not a trust boundary either. They run
as the user, so the secrets rules above keep values out of logs and accidental output, not away
from an agent that sets out to read one.

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
- For a record already held here that is set up or holds a pinned host key, `tailscale_ip`,
  `ssh_host_key`, `ssh_client_keys`, `mesh_keys` and `added_at` don't change through sync. Facts, role, notes,
  `yonder_path`, `discovered_at` and `updated_at` merge as usual. A pending record from a peer
  never replaces a set-up one (it is dropped), so sync can't unpin a host key; and `refresh`
  never finishes a pending record without a pin anyway, so nothing from a peer leads to an
  accept-new. A pending record held here without a pin is finished by the first
  full one, even an older one, so its pin is used. To change a machine's identity, run `yonder remove <m>` then
  `yonder add <m>`.
- A plain tombstone loses to a record with a newer `added_at` (a re-add). A lost tombstone
  never loses through sync. `yonder add <m>` run here clears a local lost tombstone for `m`,
  then its sync sends `"clear_lost":["m"]`; a peer drops its lost tombstone for `m` unless the
  sender is `m` itself. A peer that was offline during the re-add keeps its lost tombstone and
  wins it back on its next sync; run `yonder add <m>` again once it is online.
- Mesh hands out only the `mesh_keys` of set-up records, which sync can't change, at the address they were
  added at, owned by this account unless `--allow-shared`, and never a key a lost tombstone lists.
  A record that arrived through sync and was set up elsewhere counts: every machine trusts every
  other fully.
- Lost-key cleanup strips only keys in both the tombstone and the record this machine held for
  that name, never a key any live record lists. A machine that never held the record strips
  nothing through sync; `yonder remove <m> --lost` run there still strips the tombstone's keys.
- `_store-offer`, `_store-send` and `_store-recv` name their sender the same way and refuse an
  unknown or removed one. They also refuse when this machine has no registry yet, with exit 3
  when the store's layer is off here, exit 4 when the store is busy, and exit 5 when a mass
  delete holds it. A peer's bundle is
  checked entry by entry before anything is written, and only inside the store folder (see Store
  sync). Each machine runs the store's checks (secrets, settings names, limits, case) on what it
  receives, not only on what it sends, and decides review from its own state, not from fields
  the sender wrote.
- Incoming hooks and executables wait for a person to accept them (see Agents store). Every change
  records its origin, so `agents log` and `agents diff` say which machine changed what.
- Output, the log and `jobs/<id>.json` scrub secret values, command text included. Values that
  overlap are replaced as one span named after all of them in order, like `[A+B]`, so no part
  of either shows.
