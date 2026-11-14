#!/bin/sh
# rebuilds yonder-demo.mp4, hero.gif and poster.jpg from index.html and music.py.
# needs node 22+, google chrome, ffmpeg, python3. takes about five minutes.
set -eu
cd "$(dirname "$0")"
work=${WORK:-$(mktemp -d)}
echo "work dir: $work"

python3 -I music.py "$work/music-raw.wav"
# two-pass loudnorm to -16 LUFS, -1.5 dBTP
m=$(ffmpeg -hide_banner -i "$work/music-raw.wav" -af loudnorm=I=-16:TP=-1.5:LRA=11:print_format=json -f null - 2>&1 | sed -n '/^{/,/^}/p')
get() { printf '%s' "$m" | python3 -I -c "import json,sys; print(json.load(sys.stdin)['$1'])"; }
ffmpeg -loglevel error -y -i "$work/music-raw.wav" -af "loudnorm=I=-16:TP=-1.5:LRA=11:linear=true:measured_I=$(get input_i):measured_TP=$(get input_tp):measured_LRA=$(get input_lra):measured_thresh=$(get input_thresh):offset=$(get target_offset)" -ar 44100 "$work/music.wav"
node render.mjs frames "$work/silent.mp4" 30

# poster: the hook is fully drawn at frame 0, so the poster is frame 0
ffmpeg -loglevel error -y -i "$work/silent.mp4" -frames:v 1 -q:v 3 poster.jpg
ffmpeg -loglevel error -y -i "$work/silent.mp4" -i "$work/music.wav" -map 0:v -map 1:a \
  -c:v copy -c:a aac -b:a 160k -movflags +faststart -shortest yonder-demo.mp4

# readme gif: silent, 1280 wide, 15 fps
ffmpeg -loglevel error -y -i "$work/silent.mp4" \
  -vf "fps=15,scale=1280:-1:flags=lanczos,palettegen=max_colors=160:stats_mode=diff" "$work/pal.png"
ffmpeg -loglevel error -y -i "$work/silent.mp4" -i "$work/pal.png" \
  -lavfi "fps=15,scale=1280:-1:flags=lanczos[x];[x][1:v]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle" \
  hero.gif
ls -la yonder-demo.mp4 hero.gif poster.jpg
