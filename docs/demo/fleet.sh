#!/bin/sh
# builds the fake fleet the demo's tables come from, then prints the real
# `yonder ls`, `yonder log` and `yonder agents install` output.
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
machine laptop darwin arm64 100.64.0.2 laptop 'sleeps when the lid is shut'
cat >> "$f/home/config/machines/laptop.toml" <<'EOF'
[facts]
hostname = 'laptop'
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
cpu = 'AMD Ryzen 9 7950X 16-Core Processor'
memory_gb = 64.0
[[facts.gpus]]
name = 'NVIDIA GeForce RTX 4090'
vendor = 'nvidia'
memory_gb = 24.0
[[facts.models]]
name = 'qwen3-coder:30b'
runtime = 'ollama'
[[facts.models]]
name = 'llama3.3:70b'
runtime = 'ollama'
[[facts.models]]
name = 'nomic-embed-text'
runtime = 'ollama'
EOF
machine raspberry-pi linux arm64 100.64.0.4 other 'home automation'
cat >> "$f/home/config/machines/raspberry-pi.toml" <<'EOF'
[facts]
hostname = 'raspberry-pi'
cpu = 'Cortex-A76'
memory_gb = 8.0
EOF

seen=$(date -u -v-2H +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '2 hours ago' +%Y-%m-%dT%H:%M:%SZ)
tailnet() { # name, online for laptop and pc; raspberry-pi is always asleep
  cat > "$f/ts-$1.json" <<EOF
{"Version":"1.90.0","BackendState":"Running",
 "Self":{"ID":"nSelf","UserID":1,"HostName":"mac-mini","DNSName":"mac-mini.example.ts.net.","OS":"macOS","TailscaleIPs":["100.64.0.1"],"Online":true},
 "Peer":{
  "nodekey:a":{"UserID":1,"HostName":"laptop","DNSName":"laptop.example.ts.net.","OS":"macOS","TailscaleIPs":["100.64.0.2"],"Online":$2,"LastSeen":"$seen"},
  "nodekey:b":{"UserID":1,"HostName":"pc","DNSName":"pc.example.ts.net.","OS":"linux","TailscaleIPs":["100.64.0.3"],"Online":$2,"LastSeen":"$seen"},
  "nodekey:c":{"UserID":1,"HostName":"raspberry-pi","DNSName":"raspberry-pi.example.ts.net.","OS":"linux","TailscaleIPs":["100.64.0.4"],"Online":false,"LastSeen":"$seen"}},
 "CurrentTailnet":{"Name":"example.ts.net","MagicDNSSuffix":"example.ts.net"}}
EOF
}
tailnet true true
tailnet false false

# what the targets would have logged; peers are marked offline for `log` so it reads only this file
cat > "$f/home/state/log.jsonl" <<'EOF'
{"time":"2026-11-11T09:40:31Z","id":"20261111-094031-a9","from":"mac-mini","to":"laptop","harness":"hermes","user":"me","command":"cp mac-mini:~/Documents/Talk.key -> laptop:~/Desktop/","secrets":[],"exit":0,"ms":1840,"side":"target","copy":true,"files":23,"bytes":19293798}
{"time":"2026-11-11T09:41:12Z","id":"20261111-094112-a0","from":"mac-mini","to":"pc","harness":"hermes","user":"me","command":"python train.py","secrets":[],"exit":null,"ms":140,"side":"target","detached":true}
{"time":"2026-11-11T09:42:05Z","id":"20261111-094205-a1","from":"mac-mini","to":"pc","harness":"hermes","user":"me","command":"echo $HF_TOKEN","secrets":["HF_TOKEN"],"exit":0,"ms":96,"side":"target"}
EOF

# stand-in harness binaries so `agents install` finds all four
mkdir -p "$f/fakehome/.claude" "$f/fakehome/.codex" "$f/fakehome/.pi" "$f/fakehome/.hermes"
for b in claude codex pi hermes; do printf '#!/bin/sh\necho 1.0.0\n' > "$f/bin/$b"; chmod +x "$f/bin/$b"; done

run() { env -i PATH="$f/bin:/usr/bin:/bin" HOME="$f/fakehome" TZ=UTC YONDER_HOME="$f/home" "$@"; }
echo '$ yonder ls'
run YONDER_TAILSCALE_JSON="$f/ts-true.json" "$yonder" ls
echo '$ yonder log'
run YONDER_TAILSCALE_JSON="$f/ts-false.json" "$yonder" log
echo '$ yonder agents install'
run YONDER_TAILSCALE_JSON="$f/ts-false.json" "$yonder" agents install
