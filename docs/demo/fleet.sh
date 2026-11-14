#!/bin/sh
# builds the fake fleet the demo's terminal output comes from, then prints the
# real `yonder ls`, `yonder layers`, the laptop's rendered CLAUDE.md, `yonder log`
# and `yonder agents install` output, and the three scenes in the video: fix the
# pi, a job on the pc, a key by name.
# usage: docs/demo/fleet.sh [path/to/yonder]   (run `make build` first)
set -eu
yonder=$(cd "$(dirname "${1:-bin/yonder}")" && pwd)/$(basename "${1:-bin/yonder}")
f=$(mktemp -d)
trap 'rm -rf "$f"' EXIT
mkdir -p "$f/home/config/machines" "$f/home/state" "$f/fakehome" "$f/bin"
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)

echo "self = 'mac-mini'" > "$f/home/config/config.toml"

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
machine mac-mini darwin arm64 100.64.0.1 server 'always on'
cat >> "$f/home/config/machines/mac-mini.toml" <<'EOF'
[facts]
hostname = 'mac-mini'
os_version = 'macOS 15.5'
cpu = 'Apple M4'
memory_gb = 16.0
[[facts.gpus]]
name = 'Apple M4'
vendor = 'apple'
memory_gb = 16.0
unified = true
[[facts.harnesses]]
name = 'claude-code'
[[facts.harnesses]]
name = 'hermes'
EOF
machine laptop darwin arm64 100.64.0.2 laptop 'goes everywhere'
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
machine pc linux amd64 100.64.0.3 desktop 'heavy jobs'
cat >> "$f/home/config/machines/pc.toml" <<'EOF'
[facts]
hostname = 'pc'
os_version = 'Ubuntu 24.04'
cpu = 'AMD Ryzen 9 7950X 16-Core Processor'
memory_gb = 64.0
[[facts.gpus]]
name = 'NVIDIA GeForce RTX 4090'
vendor = 'nvidia'
memory_gb = 24.0
EOF
machine pi linux arm64 100.64.0.4 other 'home automation'
cat >> "$f/home/config/machines/pi.toml" <<'EOF'
[facts]
hostname = 'pi'
os_version = 'Debian 12'
cpu = 'Cortex-A76'
memory_gb = 8.0
EOF

seen=$(date -u -v-2H +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '2 hours ago' +%Y-%m-%dT%H:%M:%SZ)
tailnet() { # name, online for laptop and pc; pi is asleep in the ls table
  cat > "$f/ts-$1.json" <<EOF
{"Version":"1.90.0","BackendState":"Running",
 "Self":{"ID":"nSelf","UserID":1,"HostName":"mac-mini","DNSName":"mac-mini.example.ts.net.","OS":"macOS","TailscaleIPs":["100.64.0.1"],"Online":true},
 "Peer":{
  "nodekey:a":{"UserID":1,"HostName":"laptop","DNSName":"laptop.example.ts.net.","OS":"macOS","TailscaleIPs":["100.64.0.2"],"Online":$2,"LastSeen":"$seen"},
  "nodekey:b":{"UserID":1,"HostName":"pc","DNSName":"pc.example.ts.net.","OS":"linux","TailscaleIPs":["100.64.0.3"],"Online":$2,"LastSeen":"$seen"},
  "nodekey:c":{"UserID":1,"HostName":"pi","DNSName":"pi.example.ts.net.","OS":"linux","TailscaleIPs":["100.64.0.4"],"Online":false,"LastSeen":"$seen"}},
 "CurrentTailnet":{"Name":"example.ts.net","MagicDNSSuffix":"example.ts.net"}}
EOF
}
tailnet true true
tailnet false false

# what the targets would have logged; peers are marked offline for `log` so it reads only this file
cat > "$f/log-seed.jsonl" <<'EOF'
{"time":"2026-11-11T09:40:31Z","id":"20261111-094031-a9","from":"mac-mini","to":"laptop","harness":"hermes","user":"me","command":"cp mac-mini:~/Documents/Talk.key -> laptop:~/Desktop/","secrets":[],"exit":0,"ms":1840,"side":"target","copy":true,"files":23,"bytes":19293798}
{"time":"2026-11-11T09:41:12Z","id":"20261111-094112-a0","from":"mac-mini","to":"pc","harness":"hermes","user":"me","command":"python train.py","secrets":[],"exit":null,"ms":140,"side":"target","detached":true}
{"time":"2026-11-11T09:42:05Z","id":"20261111-094205-a1","from":"mac-mini","to":"pc","harness":"hermes","user":"me","command":"echo $HF_TOKEN","secrets":["HF_TOKEN"],"exit":0,"ms":96,"side":"target"}
EOF

# stand-in harness binaries so `agents install` finds all four
mkdir -p "$f/fakehome/.claude" "$f/fakehome/.codex" "$f/fakehome/.pi" "$f/fakehome/.hermes"
for b in claude codex pi hermes; do printf '#!/bin/sh\necho 1.0.0\n' > "$f/bin/$b"; chmod +x "$f/bin/$b"; done

# a short hand-written briefing in the agents store
mkdir -p "$f/home/data/agents"
cat > "$f/home/data/agents/briefing.md" <<'EOF'
reviewed: 2026-11-01

## Where things live
- Projects: `~/GitHub/<repo>` on every machine. Slides: `mac-mini:~/Documents`.
- GPU jobs go to `pc`. Services run on `mac-mini`.
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
# the same block as the laptop's claude code loads it
echo 'Be brief.' > "$f/home/data/agents/instructions.md"
echo "self = 'laptop'" > "$f/home/config/config.toml"
run YONDER_TAILSCALE_JSON="$f/ts-false.json" "$yonder" agents link >/dev/null 2>&1
echo '$ head -8 ~/.claude/CLAUDE.md   (on laptop)'
head -8 "$f/home/home/.claude/CLAUDE.md"   # under YONDER_HOME, home/ is the agents home
echo "self = 'mac-mini'" > "$f/home/config/config.toml"
echo '$ yonder log'
run YONDER_TAILSCALE_JSON="$f/ts-false.json" "$yonder" log
echo '$ yonder agents install'
run YONDER_TAILSCALE_JSON="$f/ts-false.json" "$yonder" agents install

# the video's scenes. a machine runs a command in-process when it is `self`:
# the same _exec path an ssh call reaches. so each scene makes the target self
# and runs from a home of its own.
mkdir -p "$f/homes/pi/.cache/pip" "$f/homes/pi/.cache/thumbnails" "$f/homes/pi/.cache/fontconfig" "$f/homes/pc"
dd if=/dev/zero of="$f/homes/pi/.cache/pip/wheels.bin" bs=1048576 count=2150 2>/dev/null
dd if=/dev/zero of="$f/homes/pi/.cache/thumbnails/large.bin" bs=1048576 count=46 2>/dev/null
dd if=/dev/zero of="$f/homes/pi/.cache/fontconfig/cache.bin" bs=1024 count=10 2>/dev/null
cat > "$f/homes/pc/train.py" <<'EOF'
import time
for e in range(1, 21):
    print(f"epoch {e}/20  loss {0.92 * 0.81 ** (e - 1):.3f}", flush=True)
    time.sleep(1.5)
EOF
# stand-in for the 1password cli: fills each reference with a made-up value
cat > "$f/bin/op" <<'EOF'
#!/bin/sh
[ "$1" = inject ] || exit 1
sed -E 's/[{][{] op:[^}]* [}][}]/hf_xYz3kQ9mDemoValueNotReal/'
EOF
chmod +x "$f/bin/op"
printf "HF_TOKEN = 'op://Agents/Hugging Face/credential'\n" > "$f/home/config/secrets.toml"
on() { # machine, then yonder args: run as that machine, from its home
  m=$1; shift
  printf "self = '%s'\n[secrets]\nprovider = '1password'\nmode = 'desktop'\n" "$m" > "$f/home/config/config.toml"
  (cd "$f/homes/$m" && run SHELL=/bin/sh YONDER_TAILSCALE_JSON="$f/ts-false.json" "$yonder" "$@")
}
echo '--- scene: fix the pi'
echo "\$ yonder run pi -- 'du -sh .cache/* | sort -h'"
on pi run --as hermes pi -- 'du -sh .cache/* | sort -h'
echo "\$ yonder run pi -- 'rm -rf .cache/pip && du -sh .cache'"
on pi run --as hermes pi -- 'rm -rf .cache/pip && du -sh .cache'
echo '--- scene: a job on the pc'
echo '$ yonder run pc --detach -- python3 train.py'
on pc run --as hermes --detach pc -- python3 train.py >"$f/detach.txt" 2>&1
cat "$f/detach.txt"
id=$(head -1 "$f/detach.txt")
sleep 3.4
echo "\$ yonder jobs pc $id"
on pc jobs pc "$id"
on pc jobs pc "$id" --kill >/dev/null 2>&1 || true
echo '--- scene: a key by name'
echo "\$ yonder run pc --secret HF_TOKEN -- 'echo \$HF_TOKEN'"
on pc run --as hermes --secret HF_TOKEN pc -- 'echo $HF_TOKEN'
