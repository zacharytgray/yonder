# Security

## Reporting a problem

Please report vulnerabilities privately, through GitHub:
[Report a vulnerability](https://github.com/zacharytgray/yonder/security/advisories/new).
Don't open a public issue or pull request for it.

Say what you found, which version (`yonder version`), and how to make it happen. Leave out
real hostnames, IPs and secret values. You will get a reply as soon as possible. Fixes ship in a new
release, and the advisory credits you unless you'd rather it didn't.

Only the latest release gets security fixes.

## What counts

Yonder's trust model is written down in
[docs/design.md](docs/design.md#trust-model), and the README's
[What secrets protect](README.md#what-secrets-protect) says what the secrets handling does and
doesn't cover. A report is in scope when Yonder breaks a promise made there. For example:

- a secret value showing up in a command line, the log, a job file, or output Yonder was
  meant to scrub
- a machine outside your fleet, or one you removed, getting a command run or a record accepted
- sync changing a machine's own record, a pinned host key or another identity field it
  should not change
- a provider token (`OP_SERVICE_ACCOUNT_TOKEN`, `BWS_ACCESS_TOKEN` and the like) leaking into
  a command that didn't ask for it
- `install.sh` or a release archive that can be swapped or tampered with in a way the signed
  build provenance doesn't catch

These are out of scope, because the trust model says so on purpose:

- one fleet machine attacking another. Every machine in your fleet trusts every other fully.
- an agent that sets out to read, change or print a secret value. Agents run as you.
- other programs running as your user reading a command's environment while it runs
- nodes shared in from another tailnet, or another account's nodes, once you passed
  `--allow-shared`
- problems in Tailscale, OpenSSH, 1Password or Bitwarden themselves. Report those to them.
