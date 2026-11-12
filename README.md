<div align="center">

# Yonder

**Text one agent. Fix any machine.**

Yonder connects your own computers so the AI agents on any of them can use all of them.
Ask Claude Code on your laptop why the GPU box is out of disk, and it goes and looks.

[![CI](https://github.com/zacharytgray/yonder/actions/workflows/ci.yml/badge.svg)](https://github.com/zacharytgray/yonder/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![macOS | Linux](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey.svg)

[Install](#install) · [What it does](#what-it-does) · [Secrets](#secrets) · [Commands](#commands) · [Design](docs/design.md)

</div>

```
you    ollama on the gpu box is down, can you fix it?

agent  $ yonder ls
       NAME     ROLE    STATE         HARDWARE                 MODELS  HARNESSES
       laptop   laptop  this machine  M3 Pro · 36 GB           2       claude-code,codex
       mini     server  online        M4 · 16 GB               0       claude-code,hermes
       gpu-box  gpu     online        RTX 4090 24 GB · 64 GB   3       -

       $ yonder run gpu-box -- 'systemctl --user status ollama'
       ● ollama.service - failed (out of memory)
       $ yonder run gpu-box -- 'systemctl --user restart ollama && ollama list'
       NAME              SIZE
       qwen3-coder:30b   18 GB  ...

       Ollama on gpu-box ran out of memory and stopped. I restarted it and all
       three models are back.
```

## What it does

| | |
|---|---|
| **Sees every machine** | Hardware, GPUs, free disk, local models (Ollama, LM Studio, llama.cpp, vLLM, MLX) and installed agents. Discovered, not typed. |
| **Runs anywhere** | `yonder run gpu-box -- <cmd>` over plain SSH on your tailnet. Output streams back. The exit code is the command's own. |
| **Keeps secrets off the command line** | Secrets come from 1Password or Bitwarden. The agent asks for a name like `HF_TOKEN`. The value stays out of command lines, logs and accidental output. [What that does and doesn't cover](#what-secrets-protect). |
| **Says when a machine is asleep** | An offline machine fails fast with exit code 75, when it was last seen, and your notes about it. No hanging, no retry loops. |
| **Logs what crossed machines** | Every remote command is recorded on the machine that ran it. `yonder log` merges them all. |
| **Works in every agent** | One skill, installed into Claude Code, Codex, Pi and Hermes. Each agent knows the drill without being told. |

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
only". These are optional. Your agents read them when they choose a machine. Last, it puts the
skill into every agent harness it finds.

Every prompt has a flag, so an agent can run setup too:

```sh
yonder init --yes --machines mini,gpu-box
```

## Secrets

Secrets stay in your password manager. Yonder stores only names and references, like
`op://Agents/huggingface/token`.

```sh
yonder secret setup                                  # pick a provider for this machine
yonder secret add HF_TOKEN op://Agents/huggingface/token
yonder run gpu-box --secret HF_TOKEN -- 'huggingface-cli download ...'
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
yonder secret setup --machines gpu-box          # one machine, e.g. one that was offline
yonder secret setup --machines gpu-box --replace  # give it a new token
```

Agents on those machines can read everything in that vault, so put only agent secrets in it.
The accounts are read-only unless you allow writes. Then agents can store secrets with
`yonder secret set`, but they could also change or delete anything in that vault. Setup asks;
without prompts, add `--allow-write`. `yonder doctor` says whether a machine is read-only or
read and write.

```sh
yonder secret setup --machines gpu-box --replace --allow-write
```

Yonder can't revoke a service account; `op` has no command for that. To cut a machine off, go to
1Password.com, then Developer > Service accounts. `yonder remove <machine> --lost` names the
account to revoke.

If you'd rather do it by hand, or you use Bitwarden, run `yonder secret setup` on each machine,
choose the provider, then `yonder secret token set`. To name the agent vault by hand:
`yonder secret setup --provider 1password --vault Agents`.

**About phone approval.** 1Password cannot yet approve a command-line request from its phone app.
Approval happens only in the desktop app on the machine asking. When 1Password adds phone
approval, Yonder will support it as another mode.

## Commands

| Command | What it does |
|---|---|
| `yonder init` | Set up the fleet from the machine you work from |
| `yonder ls` / `yonder show <machine>` | Machines, roles, state, hardware, models, agents. `--json` for scripts |
| `yonder run <machine> [--secret NAME] [--detach] -- <cmd>` | Run a command there |
| `yonder jobs <machine> [id]` | List detached jobs, or print one job's output |
| `yonder log` | What ran where, across every machine |
| `yonder add <machine>` | Add one more machine |
| `yonder refresh [machine]` | Rediscover facts and sync the registry everywhere |
| `yonder doctor` | Check everything and print the fix for each problem |
| `yonder secret set\|add\|ls\|rm\|test\|setup\|token` | Manage secrets. `set` stores one in the vault. `setup` makes hands-free 1Password for your machines |
| `yonder agents install\|uninstall\|status` | Manage the skill in each agent harness. `--all-machines` does every machine |
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
yonder: gpu-box is offline (last seen 2h ago) — notes: sleeps at night
```

If a laptop is lost or stolen, first remove it from your tailnet in the Tailscale admin console.
That cuts it off from every machine at once. Then run this from any other machine:

```sh
yonder remove laptop --lost
```

That removes the laptop from every machine's registry, so it can't add itself back. It deletes the
laptop's SSH keys from every other machine's `authorized_keys`, keeping a backup. Then it prints
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
don't match, skills that are out of date, machines that still trust a removed machine's key, and secrets that won't unlock. Each problem
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

`make test` runs the unit and end-to-end tests. `make integration` starts two SSH containers as a
fake fleet (needs Docker). See [docs/design.md](docs/design.md) for how the pieces fit together.

## License

MIT
