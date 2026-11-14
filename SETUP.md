# Set up Yonder (a guide for an AI agent)

A person pointed you at this repository and asked you to set Yonder up for them. This file is
for you, the agent. Follow it from top to bottom. You are not developing Yonder: ignore
`CLAUDE.md` and `AGENTS.md` here, which are for people changing Yonder's code, and don't build
it from source.

Yonder connects the person's own computers (macOS and Linux, one to five of them) over
Tailscale and SSH, so the agent on any one of them can use all of them. When you're done, every
machine will be on a list the agents can read, any machine can run commands on any other, and,
if the person wants it, their agent instructions, skills and hooks stay the same on every
machine.

## Ground rules

- Ask before every choice that is the person's to make: which machines join, which layers to
  turn on, roles and notes, how to settle instructions that differ. Use plain words. Offer a
  default when there is an obvious one.
- Some steps only the person can do: logging in to Tailscale, typing a password for
  `ssh-copy-id`, approving 1Password, typing a token, and accepting hooks. For those, tell them
  exactly what to do, where (which machine, which app, which terminal), and wait until they say
  it's done. Never try to get around one.
- Never ask the person to paste a secret, a password or a token into the chat. Never put one in a
  command line.
- Every Yonder command that fails prints the next step. Read the error and do what it says before
  trying anything else. Exit code 75 means a machine is offline or unreachable: tell the person,
  don't retry in a loop.
- Yonder needs the network: Tailscale, SSH to the other machines, and downloads. If you are
  Codex, your sandbox blocks network access by default, so `yonder` commands that reach another
  machine fail there (yonder says it is running inside Codex's sandbox). Ask the person to
  approve running them outside the sandbox (or to restart Codex with full access for this
  setup), and say why. If you are Claude Code, the person approves commands as
  you go; ask them to allow `yonder` commands for this setup.
- Run `yonder ... --help` whenever you're unsure of a flag. Every prompt Yonder has has a flag,
  and you should always use the flags (`--yes` and friends): you can't answer Yonder's
  interactive prompts.

## 1. Check what this machine has

Run these on the machine you're on (call it "this machine"):

```sh
uname -s                      # Darwin (macOS) or Linux; anything else: Yonder doesn't run there
tailscale version || /Applications/Tailscale.app/Contents/MacOS/Tailscale version
tailscale status              # or the app binary above, on a Mac with the App Store app
command -v yonder && yonder version
```

**Tailscale missing or logged out.** Yonder needs Tailscale on every machine that joins, logged
in to the same account. You can't do this for them. Say:

> Yonder uses Tailscale to find your machines. Please install it from
> https://tailscale.com/download on each computer you want to connect, open it, and log in with
> the same account on all of them. Tell me when that's done.

If `tailscale status` says it is stopped or logged out, ask them to open the Tailscale app and
log in (or run `sudo tailscale up` on Linux, which needs their password).

**SSH on this machine.** Other machines reach this one over SSH. On macOS that is Remote Login.
Check with `nc -z 127.0.0.1 22 && echo on` (or `yonder init --list` later, which reports it). If
it's off, say:

> Please turn on Remote Login: System Settings, then General, then Sharing, then Remote Login.
> (On Linux: install and start `openssh-server`.) Do the same on each computer you want to
> connect.

## 2. Install Yonder here

On a Mac with Homebrew:

```sh
brew trust --cask zacharytgray/yonder/yonder
brew install --cask zacharytgray/yonder/yonder
```

The first line is needed: Homebrew refuses casks from other taps until they are trusted. On
Linux, or a Mac without Homebrew:

```sh
curl -fsSL https://raw.githubusercontent.com/zacharytgray/yonder/main/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"   # repeat in each command if your shell doesn't keep it
```

That installs to `~/.local/bin/yonder`. If `yonder` isn't found afterwards, start each command
with `export PATH="$HOME/.local/bin:$PATH";` (your shell may not keep it between commands), and
tell the person to add that line to their shell profile. Check with `yonder version`.

You only install it here. `yonder init` installs it on the other machines itself.

## 3. Find the machines and ask which join

```sh
yonder init --list --json
```

This changes nothing. It shows:

- `self`: this machine, and `sshd` (whether SSH answers here).
- `peers`: every other device on the tailnet: `name`, `os`, `online`, `owned` (the person's
  Tailscale account owns it), `in_fleet`, `can_add`, a `note` saying why not or what to know,
  and `ssh`: `port_open` and `key_auth` (whether this machine's SSH key already gets in), with an
  `error` and a `fix` when not.
- `tailscale_error` and `next` if Tailscale isn't working.

Tell the person what you found, in plain words, and ask which machines should join. For example:

> I can see four devices on your Tailscale network: your Mac mini (this one), a Linux PC called
> pc, a MacBook called laptop (offline since yesterday), and an iPhone. Phones can't join. Which
> of pc and laptop should I connect?

Then, for each machine they picked:

- **Offline:** it can still join now. Yonder saves it as pending and finishes it with
  `yonder add <name>` once it's online. Say so.
- **`port_open` false:** ask them to turn on SSH on that machine (Remote Login on a Mac,
  `openssh-server` on Linux).
- **`key_auth` false:** this machine's SSH key isn't trusted there yet. Ask them to run, in a
  terminal on this machine (it asks for that machine's password once):

  > `ssh-copy-id <user>@<tailscale_ip>`

  If they normally use a specific key for that machine, use `--identity <name>=<path>` in step 6
  instead.
- **Different user name** on the other machines: ask, and pass `--user <name>` in step 6.
- **`owned` false:** that device belongs to another Tailscale account (shared in, or tagged).
  Only add it if the person confirms it is theirs; then it needs `--allow-shared`.
- **`add_flags`** lists flags that machine needs, such as its full name when two devices share a
  short one.

Run `yonder init --list --json` again after they fix something, to check.

## 4. Explain the layers and ask which to turn on

Connecting the machines is always on. Two optional layers sit on top. Explain them like this,
and ask which they want:

> Yonder can also do two optional things, for all your machines at once:
> - **Agent config sync (agents):** your agent instructions (CLAUDE.md, AGENTS.md), skills,
>   subagents and hooks stay the same on every machine. Each machine syncs every 15 minutes, at
>   login and on wake, with nothing running in between. Hooks from another machine wait until
>   you approve them once.
> - **Secrets (secrets):** agents can use API keys from 1Password or Bitwarden by name, without
>   ever seeing the values in commands, logs or output.
>
> You can turn either on or off later with `yonder enable` or `yonder disable`. Which would you
> like?

Ask now, too, whether you may create an SSH key on a machine that has none. A fresh machine
often has none, and without one it can't reach the others directly (step 6).

Also ask, optionally, for a role and a short note for each machine. The role is one of
`laptop`, `desktop`, `server`, `gpu`, `other`. Notes are things like "sleeps at night" or "heavy
jobs only". Agents read them to pick a machine. They can skip this.

## 5. (If they have agent config) Decide what to import

If they turned on agents, ask whether they already have skills, subagents, commands or hooks on
one machine that should go everywhere, usually in `~/.claude`. You'll import those after init.
Their instructions files (`CLAUDE.md`, `AGENTS.md`) are handled differently, in step 8, because
they often differ between machines.

## 6. Run init

Use flags only. For example:

```sh
yonder init --yes --machines pc,laptop --layers agents,secrets \
  --role pc=gpu --notes pc="heavy jobs only" --role laptop=laptop --notes laptop="travels"
```

- `--machines` lists the other machines. This machine always joins; listing it does no harm.
  Its role and notes still go in `--role` and `--notes`.
- `--layers none` for no layers. Leave out machines they didn't pick.
- Add `--user`, `--identity <machine>=<path>` or `--allow-shared` as step 3 found.
- `--no-schedule` only if they asked for no automatic sync.

Read the whole output. It ends with the problems, if any, and each says what to do. Typical:

- `ssh key auth to <m> failed; run 'ssh-copy-id ...'`: ask the person to run that line, then
  `yonder add <m>`.
- `<m> is offline ... saved as pending`: nothing to do now; `yonder add <m>` later.
- `<m> has no mesh keys on record`: that machine has no SSH key of its own, so it can't reach
  the others directly (this machine can still reach it). Ask the person if you may make one
  there. If they agree: `yonder run <m> -- 'ssh-keygen -q -t ed25519 -N "" -f ~/.ssh/id_ed25519'`,
  then `yonder add <m>`, which hands the new key to the other machines itself.
- A note that launchd or systemd didn't load the schedule: see step 10.
- "Your machines' agent instructions don't match": that's step 8.

Then run `yonder ls` and show the person their machines.

## 7. Secrets (only if they turned on secrets)

Ask which password manager they use: 1Password or Bitwarden.

**1Password.** On the machine they sit at, they need the 1Password desktop app and its CLI, with
the CLI connected to the app. Check with `op --version` and `op account list`. If either fails,
say:

> Please install the 1Password CLI (on a Mac: `brew install 1password-cli`), then in the
> 1Password app open Settings, then Developer, and turn on "Integrate with 1Password CLI". Tell
> me when that's done.

The other machines need the CLI too (`op`); `yonder secret setup` says which ones lack it. Then
run:

```sh
yonder secret setup --yes
```

Before you run it, tell them:

> 1Password will ask you to approve this once, with Touch ID or your password. It creates a vault
> called "Yonder Agents" and a read-only service account for each of your other machines, and
> puts each token straight into that machine's keychain. Neither of us sees the tokens.

If they want agents to be able to store new secrets too, add `--allow-write` (it also lets agents
change or delete items in that vault; say so).

**Bitwarden Secrets Manager.** They create a machine account and an access token in the
Bitwarden web app (Secrets Manager, then Machine accounts). You can't. Then, on each machine,
run `yonder secret setup --provider bws` (for another machine: `yonder run <m> -- yonder secret
setup --provider bws`), and ask the person to type the token themselves, in a terminal on that
machine:

> Please run `yonder secret token set` in a terminal on <machine> and paste the access token
> there. It won't be shown. Don't paste it here.

**Then:** `yonder secret ls` lists the names (never values). A program that wants a key from a
command, such as a model provider's auth helper in Codex's or another tool's config, can use
`yonder secret read NAME`: it prints the value for that program, and logs the read by name with
the harness that asked. Agents themselves use `yonder run <m> --secret NAME -- ...`, which keeps
the value out of what they see. To store a value, the person runs
`yonder secret set NAME` in their own terminal and types it into the hidden prompt. Never ask
for the value. `yonder secret test NAME` checks one without printing it.

## 8. Agent config (only if they turned on agents)

**Import shared skills, subagents, commands and hooks** from the machine they named in step 5,
not the instructions:

```sh
yonder agents import ~/.claude --kind skills --kind subagents --kind commands --kind hooks
yonder agents import ~/.agents --kind skills    # skills Codex and Pi use, if they have any
```

When the import holds hooks or other scripts, it prints them on one screen: they would wait for
review on the person's other machines, and nothing you ran can approve them. Show the person that
list. If they want them live everywhere, ask them to run, in their own terminal once the next
sync has delivered them (step 9):

    yonder agents accept --all-machines

One command, one yes. Someone who wants no review at all can set `[sync] review = "none"` in
`~/.config/yonder/config.toml` on each machine; say so only if they ask.

On another machine: `yonder run <m> -- yonder agents import '~/.claude' --kind skills ...`.
Codex hook configs go in with `yonder agents import <codex folder>/hooks.json --kind files
--harness codex`, where the Codex folder is `$CODEX_HOME` if the person set it, else `~/.codex`. Claude Code hook scripts arrive in `~/.claude/hooks/` on every machine, but Yonder never
edits `settings.json`, so the `hooks` entry that runs them stays the person's to add on each
machine (tell them which scripts arrived).

**Reconcile the instructions files.** Run:

```sh
yonder agents gather --json
```

A machine that is offline or pending isn't read, so its instructions aren't part of this
reconcile. Tell the person, and gather and reconcile again after `yonder add <m>` once it is
online.

Copy each section's `key` exactly as gather prints it, heading level included (`# style` and
`## style` are different keys).

If init said the instructions don't match, or the summary shows sections that differ or that
only some machines have, follow "Reconcile machines whose instructions differ" in the yonder
skill. init installed it at `~/.claude/skills/yonder/SKILL.md` and
`~/.agents/skills/yonder/SKILL.md`; `yonder agents reconcile --help` shows the plan format.
In short: tell the person about each difference in plain words and ask whether to share one
version, keep each machine's own on purpose, merge them, or drop it. Write the plan, show the
`--dry-run` result, and run `yonder agents reconcile --plan <file>` only after they say yes.

If every machine has the same instructions, or only one has any, make a plan that shares every
section (action `shared`, `from` that machine), show the dry run, and apply it the same way.

Where each harness gets it: Claude Code reads the instructions from a marked block in
`~/.claude/CLAUDE.md` and the skills from `~/.claude/skills/`. Codex reads `~/.codex/AGENTS.md`
(the whole file when Codex had none, else a marked block in the person's own file) and the
skills from `~/.agents/skills/`, which Pi shares. If they use `$CODEX_HOME`, run
`yonder agents link` with it set: Yonder remembers that folder and keeps rendering there, also in
the scheduled sync. A `CODEX_HOME` outside their home folder isn't supported; link refuses it, so
tell them. If Codex has an `AGENTS.override.md` with text, it reads that instead and never sees
Yonder's text: `yonder agents link` and `yonder doctor` warn about it. Tell the person, and ask
whether to move its text into the shared instructions; Yonder never writes into that file. Both
start with the same short briefing about the machines. Reconcile only takes a section out of a
harness's file when that harness is linked on the machine, so its text arrives through Yonder's
block; a file of an unlinked harness is left as it is, and reconcile says so and exits 1. Skills, subagents and commands that only Claude Code understands (`claude/agents`,
`claude/commands`) only reach Claude Code; a Codex hook config goes in `files/codex/`.

Reconcile links the store into each machine's harnesses. If you skipped reconcile, link each
machine yourself: `yonder agents link` here and `yonder run <m> -- yonder agents link` on each
other machine. Then `yonder sync`.

## 9. The one confirmation the person must type

Hooks and other executables run code, so changes from another machine wait for a person to
approve them once. You can't approve them: the command refuses without a person at a terminal,
and it should. If `yonder sync` or `yonder doctor` says changes wait for review, say:

> Some hooks or scripts from your other machines are waiting for your approval. Please run this
> in a terminal (not through me):
>
>     yonder agents accept --all-machines
>
> It shows one summary for all your machines and asks once: type `y` and press Enter to accept
> everything, or `s` to read the full diffs first.

What the person accepted, or imported at a terminal and said yes to, doesn't ask again on their other
machines; what you imported does, once.

If they use Codex: Codex also asks before running a hook that changed. Tell them to open Codex
and run `/hooks` to review and trust them.

## 10. Check everything

```sh
yonder doctor --json
```

`ok` is true when nothing failed. For each check with `status` `fail` or `warn`, the `fix` field
says what to run. Do the ones you can; tell the person about the rest. Common ones:

- `schedule`, "the timer runs only while you're logged in" (Linux): for an always-on machine the
  person runs `sudo loginctl enable-linger $USER` on it.
- `schedule`, "launchd didn't load it" on a Mac nobody is logged in to: it starts at the next
  login. Nothing to do.
- `schedule`, "no systemd here": a machine without systemd (a container, a minimal install)
  can't run the timer. It still gets every change when another machine syncs with it. Nothing
  to do; `yonder schedule remove <m>` hides the warning.
- `review`: step 9.
- `ssh` or `mesh`: run the `fix` line (`yonder mesh` lets every machine reach every other).
- `version`: install the same Yonder version on each machine (`brew upgrade --cask yonder`, or
  the install script again).

## 11. You're done

Agents read their instructions and skills when a session starts. Tell the person to start a new
Claude Code or Codex session to pick up the briefing and the yonder skill; this session won't
see them.

Tell the person what is set up (machines, layers, the schedule), and give them something to try,
in their own words, to any agent on any of their machines:

> The Raspberry Pi's disk is full, can you clean it up?

The agent will check the disk with `yonder run raspberry-pi -- df -h /`, find what is using the space,
and ask before it deletes anything. Other ideas: "Which of my machines has the
most free disk?", "Run the tests on the PC", "Is the PC busy?"

Also tell them:

- `yonder ls` lists the machines; `yonder doctor` checks everything and says how to fix it.
- To add a machine later: install Tailscale there, log in, turn on SSH, then `yonder add <name>`
  from any machine in the fleet.
- Everything is described in the README at https://github.com/zacharytgray/yonder.

## Doing it by hand

A person can do all of this without an agent: the README's Install section covers the same steps,
and `yonder init` with no flags asks every question itself.
