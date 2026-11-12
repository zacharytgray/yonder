<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/brand/yonder-logo-dark.svg">
  <source media="(prefers-color-scheme: light)" srcset="docs/brand/yonder-logo-light.svg">
  <img src="docs/brand/yonder-logo-light.svg" width="300" alt="Yonder">
</picture>

### Text one agent. Fix any machine.

Yonder connects your own computers so the AI agents on any of them can use all of them.<br>
Ask Claude Code on your laptop why the PC is out of disk, and it goes and looks.

[![CI][badge-ci]][ci]
[![Latest release][badge-release]][releases]
[![MIT license][badge-license]](LICENSE)
[![macOS and Linux][badge-platform]](#limits)

**[<kbd> <br> Install <br> </kbd>](#install)**&nbsp;
**[<kbd> <br> What it does <br> </kbd>](#what-it-does)**&nbsp;
**[<kbd> <br> Secrets <br> </kbd>](#secrets)**&nbsp;
**[<kbd> <br> Commands <br> </kbd>](#commands)**&nbsp;
**[<kbd> <br> Limits <br> </kbd>](#limits)**&nbsp;
**[<kbd> <br> Design <br> </kbd>](docs/design.md)**

<a href="docs/demo/yonder-demo.mp4">
  <img src="docs/demo/hero.gif" width="100%" alt="Text one agent, fix any machine. From a phone: I forgot my presentation on my Mac Mini, can you send it to my laptop? The agent on the Mac Mini runs yonder ls, sees the laptop, PC and a sleeping Raspberry Pi, copies Talk.key to the laptop with yonder cp, and starts a fine-tune on the PC because it has the free GPU. Then yonder secret setup with 1Password, a secret that prints as [HF_TOKEN], yonder log, a Homebrew install, and the skill going into Claude Code, Codex, Pi and Hermes.">
</a>

**[▶ Watch the 33-second demo, with sound](docs/demo/yonder-demo.mp4)**

</div>

```
you    I'm about to give a presentation but I forgot to download it on my laptop!
       It's on my Mac Mini. Can you send it over?

agent  $ yonder ls
       NAME          ROLE     STATE           HARDWARE                MODELS  HARNESSES
       laptop        laptop   online          M3 Pro · 36 GB          0       claude-code,codex
       mac-mini      server   this machine    M4 · 16 GB              0       claude-code,hermes
       pc            desktop  online          RTX 4090 24 GB · 64 GB  3       -
       raspberry-pi  other    offline 2h ago  Cortex-A76 · 8 GB       0       -

       $ yonder cp ~/Documents/Talk.key laptop:~/Desktop/
       copied Talk.key (folder, 23 files, 18.4 MB) to laptop:/Users/me/Desktop/Talk.key

       Sent. Talk.key is on your laptop's Desktop.
```

## What it does

<table>
  <tr>
    <td width="50%" valign="top">
      <h3>Sees every machine</h3>
      <p>Hardware, GPUs, free disk, local models (Ollama, LM Studio, llama.cpp, vLLM, MLX) and
      installed agents. Discovered, not typed.</p>
    </td>
    <td width="50%" valign="top">
      <h3>Runs anywhere</h3>
      <p><code>yonder run pc -- &lt;cmd&gt;</code> over plain SSH on your tailnet. Output
      streams back. The exit code is the command's own.</p>
    </td>
  </tr>
  <tr>
    <td valign="top">
      <h3>Keeps secrets off the command line</h3>
      <p>Secrets come from 1Password or Bitwarden. The agent asks for a name like
      <code>HF_TOKEN</code>. The value stays out of command lines, logs and accidental output.
      <a href="#what-secrets-protect">What that does and doesn't cover</a>.</p>
    </td>
    <td valign="top">
      <h3>Says when a machine is asleep</h3>
      <p>An offline machine fails fast with exit code 75, when it was last seen, and your notes
      about it. No hanging, no retry loops.</p>
    </td>
  </tr>
  <tr>
    <td valign="top">
      <h3>Logs what crossed machines</h3>
      <p>Every remote command is recorded on the machine that ran it. <code>yonder log</code>
      merges them all.</p>
    </td>
    <td valign="top">
      <h3>Works in every agent</h3>
      <p>One skill, installed into Claude Code, Codex, Pi and Hermes. Each agent knows the drill
      without being told.</p>
    </td>
  </tr>
  <tr>
    <td valign="top">
      <h3>Syncs agent config</h3>
      <p>Optional: one set of instructions, skills, subagents and hooks on every machine, wired
      into each harness. Peer to peer over the same SSH, with history and no server.</p>
    </td>
    <td valign="top">
      <h3>Takes only what you turn on</h3>
      <p>Connecting machines is always on. Secrets and agent config are layers you switch on or
      off for the whole fleet with one command.</p>
    </td>
  </tr>
</table>

Yonder connects machines. It does not decide what your agent may do. Your harness already does
that, with its own permission modes.

## Install

You need [Tailscale](https://tailscale.com/download) on each machine, and SSH turned on for the
machines you want to reach (on macOS: System Settings → General → Sharing → Remote Login).

```sh
brew trust --cask zacharytgray/yonder/yonder
brew install --cask zacharytgray/yonder/yonder
```

Homebrew refuses to load casks from third-party taps until you trust them. The first line trusts
only the Yonder cask, which also lets `brew upgrade --cask yonder` work later.

Or, on any Mac or Linux machine without Homebrew:

```sh
curl -fsSL https://raw.githubusercontent.com/zacharytgray/yonder/main/install.sh | sh
```

Each release archive has a signed build provenance. To check one you downloaded:
`gh attestation verify yonder_<version>_<os>_<arch>.tar.gz --repo zacharytgray/yonder`.
The macOS binaries are also signed with a Developer ID and notarized by Apple, so Gatekeeper
lets them run.

Then, on the machine you work from:

```sh
yonder init
```

`init` finds the machines on your tailnet, lets you pick which ones join, checks SSH to each one
(offering to copy your key if it is missing), installs Yonder on them, and discovers what each
one has. It asks for each machine's role and any notes, such as "sleeps at night" or "heavy jobs
only". These are optional. Your agents read them when they choose a machine. Change them later
with `yonder set <machine> --role server --notes "always on"`. Then it offers to
let every machine SSH into every other one, so an agent on the laptop can reach the PC even
though you only copied your key from the machine you ran `init` on. Last, it puts the skill into
every agent harness it finds.

`yonder add` makes the same offer for the machine it just added. Each machine is trusted only
with the key it actually uses to connect: its default SSH key (`~/.ssh/id_ed25519` and the other
names SSH tries on its own), plus any key you set for it with `yonder ssh-key`. Other keys in its
`~/.ssh`, such as a GitHub deploy key, are never handed out. Yonder backs up each
`~/.ssh/authorized_keys` first and marks every line it adds with `yonder:<machine>`. It only ever
removes lines with that mark, and never touches lines you added yourself. `yonder mesh` does the
whole fleet again, for example after a machine that was asleep comes back, and removes marked
lines that are no longer needed. `--no-mesh` skips it.

Every prompt has a flag, so an agent can run setup too:

```sh
yonder init --yes --machines mac-mini,pc
```

## Send files

```sh
yonder cp ~/Documents/Talk.key laptop:~/Desktop/
```

Each side is a local path or `<machine>:<path>`, like scp. Folders copy whole, so a Keynote file
or an app bundle just works. `~` is the home on the receiving machine. An existing file or folder
is never replaced without `--force`. A copy between two other machines passes through the one you
run it on. The receiving machine logs each copy, and `yonder log` shows it with its size.

## Layers

Connecting your machines is always on. On top of it sit optional layers, each one fleet-wide:

| Layer | What it adds |
|---|---|
| `secrets` | Commands that use secrets from 1Password or Bitwarden |
| `agents` | Agent instructions, skills, subagents and hooks, synced between machines |

`yonder init` asks which ones you want, or takes `--layers secrets,agents`. Change them any time:

```sh
yonder layers              # what is on
yonder enable agents       # on for every machine
yonder disable secrets     # off for every machine; nothing is deleted
```

Machines that are offline pick up the change at their next sync. Turning a layer off keeps its
data. With `secrets` off, `--secret` is refused with a note on how to turn it back on. With
`agents` off, each machine unlinks its harnesses and puts back what linking replaced. If you used
secrets before layers existed, secrets stays on.

## Secrets

Secrets stay in your password manager. Yonder stores only names and references, like
`op://Agents/huggingface/token`.

```sh
yonder secret setup                                  # pick a provider for this machine
yonder secret add HF_TOKEN op://Agents/huggingface/token
yonder run pc --secret HF_TOKEN -- 'huggingface-cli download ...'
```

With 1Password you can skip the mapping. Yonder records one vault for agent secrets. An item
in it titled `HF_TOKEN` is the secret `HF_TOKEN` on every machine that reads the vault. A name
resolves from `secrets.toml` first, then from the vault. `yonder secret ls` lists both.

**Store a secret without seeing it.** `yonder secret set` puts a new value in that vault. It
reads the value from a pipe or a hidden prompt, so the value never lands on a command line, in
the output or in the log. An agent can store a token it just made this way.

```sh
some-cli new-token | yonder secret set NEW_TOKEN     # what an agent runs
yonder secret set HF_TOKEN                           # you type it, hidden
yonder secret set HF_TOKEN --replace                 # change one that exists
```

In desktop mode you approve once. A service account can store secrets only if you let it at
setup (`--allow-write`, below). Bitwarden can't: `bws` takes the value on its command line.

**Hand a key to a program.** Some programs want a key printed on stdout, such as an auth
helper in a config file. `yonder secret read NAME` prints the value and one newline, like
`op read`. It resolves on this machine, and the read is logged by name. Agents should use
`run --secret`, which keeps the value out of what they see.

```toml
auth_command = "yonder secret read MODEL_API_KEY"   # in some program's config
```

The machine your agent runs on unlocks the value. The value travels over SSH stdin into the
remote command's environment. Yonder never puts it in a command line or a log (a program your
command runs could still pass it on its own command line). If a command prints
it, the agent sees `[HF_TOKEN]` in its place. While the command runs, other programs running as
your user on that machine can read its environment, as with any environment variable.

### What secrets protect

Yonder keeps values out of the places they leak by accident. It does not keep them from an agent
that sets out to get one.

Protected:

- Command lines, shell history and `ps`, as far as Yonder's own transport goes: values travel on
  stdin and in the environment. A tool your command runs can still put a value in its own
  arguments.
- Yonder's log and job files. They hold secret names, never values.
- Output that happens to include a value. Yonder replaces the exact value with `[NAME]`. It also
  catches the value trimmed and each line of a multi-line value, when that is 8 or more
  characters, and the value JSON-escaped.
- Provider tokens. A command run through Yonder never inherits `OP_SERVICE_ACCOUNT_TOKEN`,
  `BWS_ACCESS_TOKEN` or the like from Yonder's own environment, unless you ask for that name with
  `--secret`.

Not protected:

- An agent that deliberately reads, changes or prints a value. `echo $HF_TOKEN | rev` prints it
  reversed, and the scrubber can't know.
- Anything the agent can read as you. Agents run as your user, so they can read your keychain
  entry or token file for the service account, and the environment of your other programs.
- `yonder secret read` doesn't change this: an agent allowed a secret could already read it.

What one approval reaches depends on the mode:

- **Desktop mode** reaches any vault your signed-in 1Password session can read. `yonder secret
  add` takes any `op://` reference, so an agent on that machine could name an item in your
  Private vault.
- **Service-account mode** reaches only the agent vault the account was granted.

If that matters, use service accounts on machines where agents run unattended, and keep only
agent secrets in the agent vault.

You choose how secrets unlock on each machine:

| Mode | How you approve | Good for |
|---|---|---|
| **1Password desktop** | Touch ID, Apple Watch or your system password, through the 1Password app. One approval lasts until 10 minutes idle, 12 hours at most. | A machine you sit at |
| **1Password service account** | Nothing. A read-only token for one vault you create for agents. | Servers and machines nobody sits at |
| **Bitwarden Secrets Manager** | Nothing. A machine-account access token. | Same |

Tokens for the hands-free modes are kept in the macOS Keychain, the Linux secret service, or a
file only you can read, in that order.

**Hands-free 1Password in one command.** Run this on the machine you sit at, the one with the
1Password app:

```sh
yonder secret setup
```

Choose the hands-free option. Yonder creates a vault for agent secrets, "Yonder Agents" by
default. Then it creates one read-only service account per machine, named `yonder-<machine>`,
and stores each token in that machine's keychain. You approve once, with Touch ID or your
password. You don't visit the 1Password website. Each token goes straight to its own machine. It
never lands on a command line, in a log or on another machine's disk.

By default it sets up every other online machine. The machine you sit at keeps Touch ID. Every
machine needs the 1Password CLI, `op`; yonder skips one without it and tells you how to install
it. Without prompts:

```sh
yonder secret setup --yes                       # the default machines
yonder secret setup --machines raspberry-pi     # one machine, e.g. one that was offline
yonder secret setup --machines pc --replace     # give it a new token
```

Agents on those machines can read everything in that vault, so put only agent secrets in it.
The accounts are read-only unless you allow writes. Then agents can store secrets with
`yonder secret set`, but they could also change or delete anything in that vault. Setup asks;
without prompts, add `--allow-write`. `yonder doctor` says whether a machine is read-only or
read and write.

```sh
yonder secret setup --machines pc --replace --allow-write
```

**Secrets only some machines should read.** Give those machines read access to one more vault
with `--extra-vault`. Their new accounts read the agent vault as before, and that vault too,
read-only. A name resolves from the agent vault first, then from each extra vault in order.
`yonder secret set` still writes only to the agent vault, so you fill an extra vault in
1Password itself. `yonder secret ls` and `yonder doctor` show the extra vaults.

```sh
yonder secret setup --machines server --vault Agents --extra-vault Ops --replace
```

A vault that no service account can read stays behind Touch ID. Reach it from the machine you
sit at with a reference in `secrets.toml`: `yonder secret add NAME op://Gated/item/field`.

Yonder can't revoke a service account; `op` has no command for that. To cut a machine off, go to
1Password.com, then Developer > Service accounts. `yonder remove <machine> --lost` names the
account to revoke.

If you'd rather do it by hand, or you use Bitwarden, run `yonder secret setup` on each machine,
choose the provider, then `yonder secret token set`. To name the agent vault by hand:
`yonder secret setup --provider 1password --vault Agents`.

**About phone approval.** 1Password cannot yet approve a command-line request from its phone app.
Approval happens only in the desktop app on the machine asking. When 1Password adds phone
approval, Yonder will support it as another mode.

## Agent config

With the `agents` layer on, one folder, `~/.local/share/yonder/agents`, holds your agent config and
syncs between your machines:

```sh
yonder agents import ~/.claude                # CLAUDE.md, skills/, agents/, commands/, hooks/
yonder agents link                            # wire it into Claude Code, Codex and Pi here
yonder sync                                   # send it to every online machine
```

`instructions.md` is shared by every harness, and `instructions.claude.md` or
`instructions.codex.md` add text for one. `machines/<name>/` holds what only one machine gets.

`link` never edits a settings file. In `~/.claude/CLAUDE.md` it writes the instructions into a
marked block and keeps your own text. It renders `~/.codex/AGENTS.md` and `~/.pi/agent/AGENTS.md`
the same way, and links each skill, subagent, command and hook. After you edit the store, the
next `yonder sync` renders the new text in on every machine. Anything in the way is backed up first.
`yonder agents unlink` puts it all back.

How sync behaves:

- Every file carries a version counter per machine. If two machines edit a file before they sync,
  both versions are kept. The later one stays, and the other sits beside it as
  `<name>.conflict-<machine>-<date>.<ext>`. Settle it with
  `yonder sync --resolve <path> --keep mine|theirs`.
- Hooks run code, so new or changed ones from another machine wait until you run
  `yonder agents accept` in a terminal. So do executables and files linked into a harness's own
  folder. `[sync] review` in `~/.config/yonder/config.toml` changes that: `executables` (the
  default), `all` or `none`.
- Secrets and settings never sync. Names like `.env`, `*.pem` or a harness's `settings.json` and
  `config.toml` are refused, and so are files that look like they hold a key, with the line
  number.
- If a sync would delete most of the store, it stops and asks for `--allow-deletes`: a moved
  or missing folder looks like that.
- Replaced and deleted versions are kept: `yonder agents restore <path>`. `yonder agents log`
  and `yonder agents diff` show which machine changed what.
- There is no daemon. Sync runs on `yonder sync`, `refresh` and `add`, and after a change made
  through yonder. To sync at the start of each agent session, have the harness run
  `yonder sync --background`; for Claude Code, a SessionStart hook does it.
- Two machines that are never awake at the same time only meet through a third one. A cron
  entry running `yonder sync` on an always-on machine covers that.
- A machine away for more than 90 days holds back its old files, since the others may have
  deleted them meanwhile. `yonder doctor` lists them, and `yonder sync --stale-ok` sends them.

## Commands

| Command | What it does |
|---|---|
| `yonder init` | Set up the fleet from the machine you work from |
| `yonder ls` / `yonder show <machine>` | Machines, roles, state, hardware, models, agents. `--json` for scripts |
| `yonder run <machine> [--secret NAME] [--detach] -- <cmd>` | Run a command there |
| `yonder cp [--force] <src>... <dest>` | Send files and folders between machines, like scp. Each side is a path or `<machine>:<path>` |
| `yonder jobs <machine> [id] [--follow\|--kill]` | List detached jobs, or print one job's output. `--follow` waits for it and exits with its code. `--kill` stops it |
| `yonder log` | What ran where, across every machine |
| `yonder add <machine>` | Add one more machine |
| `yonder set <machine> [--role R] [--notes TEXT]` | Change a machine's role or notes |
| `yonder mesh [machine]` | Let every machine SSH into every other one |
| `yonder refresh [machine]` | Rediscover facts and sync the registry everywhere |
| `yonder doctor` | Check everything and print the fix for each problem |
| `yonder secret set\|add\|ls\|rm\|test\|setup\|token` | Manage secrets. `set` stores one in the vault. `setup` makes hands-free 1Password for your machines |
| `yonder agents install\|uninstall\|status` | Manage the skill in each agent harness. `--all-machines` does every machine |
| `yonder layers` / `yonder enable\|disable <layer>` | See and switch the optional layers for the whole fleet |
| `yonder sync [store] [machine...]` | Sync the registry and the stores of the layers that are on. `--resolve` settles a conflict |
| `yonder agents import\|link\|unlink\|log\|diff\|accept\|restore\|allow` | Agent config: put it in the store, wire it into the harnesses, see and undo changes, review hooks |
| `yonder remove <machine> [--lost]` | Take a machine out of the fleet |

## How agents learn it

`yonder agents install` copies one [skill](skill/yonder/SKILL.md) into each harness's own skills
folder:

| Harness | Folder |
|---|---|
| Claude Code | `~/.claude/skills/yonder/` |
| Codex, Pi | `~/.agents/skills/yonder/` |
| Hermes | `~/.hermes/skills/yonder/` |

It writes only that folder and never edits a harness's settings. The skill costs no context until
an agent needs another machine. Then it tells the agent to read `yonder ls`, pick a machine by its
role and notes, use `yonder run`, refer to secrets by name, and treat exit code 75 as "tell the
user" instead of "try again".

## When a machine is asleep or lost

Tailscale cannot tell a sleeping laptop from one that is switched off, so Yonder doesn't guess.
It says `offline`, shows when the machine was last seen, and adds your notes:

```
yonder: raspberry-pi is offline (last seen 2h ago) — notes: sleeps at night
```

If a laptop is lost or stolen, first remove it from your tailnet in the Tailscale admin console.
That cuts it off from every machine at once. Then run this from any other machine:

```sh
yonder remove laptop --lost
```

That removes the laptop from every machine's registry, so it can't add itself back. It deletes the
laptop's SSH keys from every other machine's `authorized_keys`, keeping a backup. A plain
`yonder remove` deletes only the lines `yonder mesh` added for that machine. Then it prints
what's left, such as revoking the laptop's secrets token if it had one. Machines that were offline
clean themselves up the next time they sync, for example on `yonder refresh`.

If another machine uses the same SSH key as the laptop, Yonder leaves that key in place and tells
you which machines share it. Add `--include-shared` to remove it anyway, then give those machines
a new key.

All your machines trust each other fully. Yonder doesn't try to defend one machine in your fleet
against another. See the [trust model](docs/design.md#trust-model).

## Keeping it fresh

`yonder ls` refreshes any online machine whose facts are more than a day old. `yonder doctor`
flags machines that are unreachable or offline, facts more than a week old, Yonder versions that
don't match, skills that are out of date, a machine that can't SSH into another one, machines that
still trust a removed machine's key, secrets that won't unlock, and agent config that won't sync,
is in conflict or waits for review. Each problem
comes with the command that fixes it.

## Limits

- macOS and Linux only. No Windows yet.
- Tailscale is required. Yonder uses it to find your machines and to know which ones are online.
- No wake-on-LAN. Tailscale cannot wake a sleeping machine.
- Yonder is for your own one to five machines. It is not a team tool or a job scheduler.
- Yonder assumes a single-owner tailnet: your machines only. It refuses nodes another account
  owns (shared in from another tailnet, another user's, or tagged: tagged devices count as not
  yours) unless you pass `--allow-shared`, and names that match more than one machine unless you
  pass the full name.

## Contributing

Bug reports, ideas and pull requests are welcome. `make test` runs the unit and end-to-end
tests. `make integration` starts two SSH containers as a fake fleet (needs Docker). See
[CONTRIBUTING.md](CONTRIBUTING.md) for the ground rules, [docs/design.md](docs/design.md) for
how the pieces fit together, and [docs/brand.md](docs/brand.md) for the look.

Found a security problem? Please report it privately. See [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)

[ci]: https://github.com/zacharytgray/yonder/actions/workflows/ci.yml
[releases]: https://github.com/zacharytgray/yonder/releases
[badge-ci]: https://img.shields.io/github/actions/workflow/status/zacharytgray/yonder/ci.yml?branch=main&style=flat-square&label=ci&labelColor=26223d
[badge-release]: https://img.shields.io/github/v/release/zacharytgray/yonder?style=flat-square&label=release&labelColor=26223d&color=d2602f
[badge-license]: https://img.shields.io/github/license/zacharytgray/yonder?style=flat-square&label=license&labelColor=26223d&color=d2602f
[badge-platform]: https://img.shields.io/badge/platform-macOS%20%7C%20Linux-d2602f?style=flat-square&labelColor=26223d
