#!/bin/sh
# rebuilds yonder-demo.mp4, hero.gif and poster.jpg from index.html and music.py.
# needs node 22+, google chrome, ffmpeg, python3. takes about ten minutes.
set -eu
cd "$(dirname "$0")"
work=${WORK:-$(mktemp -d)}
echo "work dir: $work"

python3 -I music.py "$work/music.wav"
node render.mjs frames "$work/silent.mp4" 30

# poster: the settled title card, baked in as frame 0
ffmpeg -loglevel error -y -ss 8.8 -i "$work/silent.mp4" -frames:v 1 "$work/poster.png"
ffmpeg -loglevel error -y -i "$work/poster.png" -q:v 3 poster.jpg
ffmpeg -loglevel error -y -i "$work/silent.mp4" -i "$work/poster.png" -i "$work/music.wav" \
  -filter_complex "[0:v][1:v]overlay=0:0:enable='eq(n,0)'[v]" -map "[v]" -map 2:a \
  -c:v libx264 -preset slow -crf 20 -pix_fmt yuv420p -c:a aac -b:a 160k -movflags +faststart \
  -shortest yonder-demo.mp4

# readme gif: silent, 1280 wide, 15 fps
ffmpeg -loglevel error -y -i "$work/silent.mp4" \
  -vf "fps=15,scale=1280:-1:flags=lanczos,palettegen=max_colors=160:stats_mode=diff" "$work/pal.png"
ffmpeg -loglevel error -y -i "$work/silent.mp4" -i "$work/pal.png" \
  -lavfi "fps=15,scale=1280:-1:flags=lanczos[x];[x][1:v]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle" \
  hero.gif
ls -la yonder-demo.mp4 hero.gif poster.jpg
