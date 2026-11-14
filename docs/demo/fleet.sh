#!/bin/sh
# builds the fake fleet the demo's terminal output comes from, then prints the
# real `yonder ls`, `yonder layers`, `yonder log` and `yonder agents install`
# output, and the scenes in the video: the thesis on the desktop, tests on the
# gaming pc, a check and a key from the phone, the same rules everywhere.
# usage: docs/demo/fleet.sh [path/to/yonder]   (run `make build` first)
set -eu
yonder=$(cd "$(dirname "${1:-bin/yonder}")" && pwd)/$(basename "${1:-bin/yonder}")
f=$(mktemp -d)
trap 'rm -rf "$f"' EXIT
mkdir -p "$f/home/config/machines" "$f/home/state" "$f/fakehome" "$f/bin"
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)

echo "self = 'laptop'" > "$f/home/config/config.toml"

machine() { # name os arch ip role notes
  cat > "$f/home/config/machines/$1.toml" <<EOF
name = '$1'
os = '$2'
arch = '$3'
tailscale_ip = '$4'
dns_name = '$1.example.ts.net'
ssh_user = 'me'
ssh_port = 22
yonder_path = '/home/me/.local/bin/yonder'
role = '$5'
notes = '$6'
updated_at = $now
discovered_at = $now
added_at = $now
EOF
}
machine laptop darwin arm64 100.64.0.1 laptop 'goes everywhere'
cat >> "$f/home/config/machines/laptop.toml" <<'EOF'
[facts]
hostname = 'laptop'
os_version = 'macOS 15.5'
laptop = true
cpu = 'Apple M3 Pro'
memory_gb = 36.0
[[facts.gpus]]
name = 'Apple M3 Pro'
vendor = 'apple'
memory_gb = 36.0
unified = true
[[facts.harnesses]]
name = 'claude-code'
[[facts.harnesses]]
name = 'codex'
EOF
machine desktop darwin arm64 100.64.0.2 desktop 'thesis lives here'
cat >> "$f/home/config/machines/desktop.toml" <<'EOF'
[facts]
hostname = 'desktop'
os_version = 'macOS 15.5'
cpu = 'Apple M4'
memory_gb = 24.0
[[facts.gpus]]
name = 'Apple M4'
vendor = 'apple'
memory_gb = 24.0
unified = true
[[facts.harnesses]]
name = 'codex'
EOF
machine gaming-pc linux amd64 100.64.0.3 gpu 'heavy jobs'
cat >> "$f/home/config/machines/gaming-pc.toml" <<'EOF'
[facts]
hostname = 'gaming-pc'
os_version = 'Ubuntu 24.04'
cpu = 'AMD Ryzen 9 7950X 16-Core Processor'
memory_gb = 64.0
[[facts.gpus]]
name = 'NVIDIA GeForce RTX 4090'
vendor = 'nvidia'
memory_gb = 24.0
[[facts.harnesses]]
name = 'claude-code'
EOF
machine raspberry-pi linux arm64 100.64.0.4 server 'always on'
cat >> "$f/home/config/machines/raspberry-pi.toml" <<'EOF'
[facts]
hostname = 'raspberry-pi'
os_version = 'Debian 12'
cpu = 'Cortex-A76'
memory_gb = 8.0
[[facts.harnesses]]
name = 'hermes'
EOF

seen=$(date -u -v-2H +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '2 hours ago' +%Y-%m-%dT%H:%M:%SZ)
tailnet() { # online: true for the ls table, false for everything else
  cat > "$f/ts-$1.json" <<EOF
{"Version":"1.90.0","BackendState":"Running",
 "Self":{"ID":"nSelf","UserID":1,"HostName":"laptop","DNSName":"laptop.example.ts.net.","OS":"macOS","TailscaleIPs":["100.64.0.1"],"Online":true},
 "Peer":{
  "nodekey:a":{"UserID":1,"HostName":"desktop","DNSName":"desktop.example.ts.net.","OS":"macOS","TailscaleIPs":["100.64.0.2"],"Online":$1,"LastSeen":"$seen"},
  "nodekey:b":{"UserID":1,"HostName":"gaming-pc","DNSName":"gaming-pc.example.ts.net.","OS":"linux","TailscaleIPs":["100.64.0.3"],"Online":$1,"LastSeen":"$seen"},
  "nodekey:c":{"UserID":1,"HostName":"raspberry-pi","DNSName":"raspberry-pi.example.ts.net.","OS":"linux","TailscaleIPs":["100.64.0.4"],"Online":$1,"LastSeen":"$seen"}},
 "CurrentTailnet":{"Name":"example.ts.net","MagicDNSSuffix":"example.ts.net"}}
EOF
}
tailnet true
tailnet false

# what the targets would have logged; peers are marked offline for `log` so it reads only this file
cat > "$f/log-seed.jsonl" <<'EOF'
{"time":"2026-11-11T09:40:31Z","id":"20261111-094031-a9","from":"laptop","to":"desktop","harness":"claude-code","user":"me","command":"git -C ~/thesis log --oneline -3","secrets":[],"exit":0,"ms":212,"side":"target"}
{"time":"2026-11-11T09:41:12Z","id":"20261111-094112-a0","from":"laptop","to":"gaming-pc","harness":"claude-code","user":"me","command":"cd ~/thesis && python3 -m unittest -q","secrets":[],"exit":null,"ms":140,"side":"target","detached":true}
{"time":"2026-11-11T09:42:05Z","id":"20261111-094205-a1","from":"raspberry-pi","to":"gaming-pc","harness":"hermes","user":"me","command":"hf auth whoami","secrets":["HF_TOKEN"],"exit":0,"ms":96,"side":"target"}
EOF

# stand-in harness binaries so `agents install` finds all four
mkdir -p "$f/fakehome/.claude" "$f/fakehome/.codex" "$f/fakehome/.pi" "$f/fakehome/.hermes"
for b in claude codex pi hermes; do printf '#!/bin/sh\necho 1.0.0\n' > "$f/bin/$b"; chmod +x "$f/bin/$b"; done

# a short hand-written briefing in the agents store
mkdir -p "$f/home/data/agents"
cat > "$f/home/data/agents/briefing.md" <<'EOF'
reviewed: 2026-11-01

## Where things live
- Projects: `~/<repo>` on every machine. The thesis: `desktop:~/thesis`.
- Tests and GPU jobs go to `gaming-pc`.
- Secrets by name only: `yonder run <machine> --secret NAME -- <cmd>`.
EOF

run() { env -i PATH="$f/bin:/usr/bin:/bin" HOME="$f/fakehome" TZ=UTC YONDER_HOME="$f/home" "$@"; }
# both layers on (peers offline here, so nothing syncs)
run YONDER_TAILSCALE_JSON="$f/ts-false.json" "$yonder" enable secrets agents >/dev/null
cp "$f/log-seed.jsonl" "$f/home/state/log.jsonl"   # enable logs its sync attempts; keep only the seed
echo '$ yonder ls'
run YONDER_TAILSCALE_JSON="$f/ts-true.json" "$yonder" ls
echo '$ yonder layers'
run YONDER_TAILSCALE_JSON="$f/ts-false.json" "$yonder" layers
echo '$ yonder log'
run YONDER_TAILSCALE_JSON="$f/ts-false.json" "$yonder" log
echo '$ yonder agents install'
run YONDER_TAILSCALE_JSON="$f/ts-false.json" "$yonder" agents install

# the video's scenes. a machine runs a command in-process when it is `self`:
# the same _exec path an ssh call reaches. so each scene makes the target self
# and runs from a home of its own.
mkdir -p "$f/homes/laptop" "$f/homes/desktop" "$f/homes/gaming-pc" "$f/homes/raspberry-pi"
# the thesis repo on the desktop, with fixed dates so the hashes stay the same
g() { # date, then git args
  d=$1; shift
  env -i PATH=/usr/bin:/bin HOME="$f/fakehome" GIT_CONFIG_NOSYSTEM=1 \
    GIT_AUTHOR_NAME=me GIT_AUTHOR_EMAIL=me@example.com GIT_COMMITTER_NAME=me GIT_COMMITTER_EMAIL=me@example.com \
    GIT_AUTHOR_DATE="$d" GIT_COMMITTER_DATE="$d" git -C "$f/homes/desktop/thesis" "$@"
}
mkdir -p "$f/homes/desktop/thesis"
g 2026-11-10T16:00:00Z init -q -b main
for c in '2026-11-10T16:02:00Z|Fix the data loader seed' '2026-11-12T11:30:00Z|Add the BLEU eval script' '2026-11-13T21:47:00Z|Start the chapter 4 results table'; do
  echo "${c#*|}" >> "$f/homes/desktop/thesis/NOTES"
  g "${c%%|*}" add NOTES
  g "${c%%|*}" commit -q -m "${c#*|}"
done
# the gaming pc's copy: 212 small tests that take a few seconds
mkdir -p "$f/homes/gaming-pc/thesis/tests"
: > "$f/homes/gaming-pc/thesis/tests/__init__.py"
for mod in data eval model; do
  cat > "$f/homes/gaming-pc/thesis/tests/test_$mod.py" <<'EOF'
import time
import unittest

class T(unittest.TestCase):
    pass

def case(i):
    def t(self):
        time.sleep(0.01)
        self.assertEqual((i * 7) % 7, 0)
    return t
EOF
done
printf 'for i in range(71):\n    setattr(T, f"test_{i}", case(i))\n' >> "$f/homes/gaming-pc/thesis/tests/test_data.py"
printf 'for i in range(71):\n    setattr(T, f"test_{i}", case(i))\n' >> "$f/homes/gaming-pc/thesis/tests/test_eval.py"
printf 'for i in range(70):\n    setattr(T, f"test_{i}", case(i))\n' >> "$f/homes/gaming-pc/thesis/tests/test_model.py"
# stand-in for the 1password cli: fills each reference with a made-up value
cat > "$f/bin/op" <<'EOF'
#!/bin/sh
[ "$1" = inject ] || exit 1
sed -E 's/[{][{] op:[^}]* [}][}]/hf_xYz3kQ9mDemoValueNotReal/'
EOF
chmod +x "$f/bin/op"
# stand-in for `hf auth whoami` (huggingface_hub 2.x): prints the account the token belongs to
cat > "$f/bin/hf" <<'EOF'
#!/bin/sh
[ "$1 $2" = 'auth whoami' ] || exit 1
[ -n "${HF_TOKEN:-}" ] || { echo 'Error: Not logged in. Run `hf auth login` first.' >&2; exit 1; }
printf '\342\234\223 Logged in\n  user: me\n'
EOF
chmod +x "$f/bin/hf"
printf "HF_TOKEN = 'op://Agents/Hugging Face/credential'\n" > "$f/home/config/secrets.toml"
on() { # machine, then yonder args: run as that machine, from its home
  m=$1; shift
  printf "self = '%s'\n[secrets]\nprovider = '1password'\nmode = 'desktop'\n" "$m" > "$f/home/config/config.toml"
  (cd "$f/homes/$m" && env -i PATH="$f/bin:/usr/bin:/bin" HOME="$f/homes/$m" TZ=UTC YONDER_HOME="$f/home" \
    SHELL=/bin/sh YONDER_TAILSCALE_JSON="$f/ts-false.json" "$yonder" "$@")
}

echo '--- scene: the coding agent on the laptop'
echo "\$ yonder run desktop -- 'git -C ~/thesis log --oneline -3'"
on desktop run --as claude-code desktop -- 'git -C ~/thesis log --oneline -3'
echo "\$ yonder run gaming-pc --detach -- 'cd ~/thesis && python3 -m unittest -q'"
on gaming-pc run --as claude-code --detach gaming-pc -- 'cd ~/thesis && python3 -m unittest -q' >"$f/detach.txt" 2>&1
cat "$f/detach.txt"
id=$(head -1 "$f/detach.txt")
# wait for the tests to finish
while ! grep -q exited "$f/home/state/jobs/$id.json" 2>/dev/null; do sleep 0.5; done
echo '--- scene: a text from the phone, to the agent on the raspberry pi'
echo '$ yonder jobs gaming-pc'
on gaming-pc jobs gaming-pc
echo "\$ yonder jobs gaming-pc $id"
on gaming-pc jobs gaming-pc "$id"

echo '--- scene: the same rules on every machine'
# the house rules in the agents store. the laptop's agent adds the last one; sync
# carries the store to every machine, and link renders it into each harness's file
printf '%s\n' '## House rules' '- Be brief.' '- Ask before deleting anything.' '- Run the tests before you commit.' \
  > "$f/home/data/agents/instructions.md"
echo '$ tail -4 ~/.local/share/yonder/agents/instructions.md   (on desktop)'
tail -4 "$f/home/data/agents/instructions.md"
printf "self = 'desktop'\n" > "$f/home/config/config.toml"
mkdir -p "$f/home/home/.codex"   # under YONDER_HOME, home/ is the agents home
run YONDER_TAILSCALE_JSON="$f/ts-false.json" "$yonder" agents link >/dev/null 2>&1
echo '$ tail -4 ~/.codex/AGENTS.md   (on desktop)'
tail -4 "$f/home/home/.codex/AGENTS.md"

echo '--- scene: a key by name'
echo '$ yonder run gaming-pc --secret HF_TOKEN -- hf auth whoami'
on gaming-pc run --as hermes --secret HF_TOKEN gaming-pc -- hf auth whoami
