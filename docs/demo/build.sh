#!/bin/sh
# rebuilds yonder-demo.mp4, hero.gif and poster.jpg from index.html and music.m4a.
# music.m4a is already cut and leveled (music.sh made it). needs node 22+, google
# chrome and ffmpeg. takes about seven minutes.
set -eu
cd "$(dirname "$0")"
work=${WORK:-$(mktemp -d)}
echo "work dir: $work"

node render.mjs frames "$work/silent.mp4" 30

# poster: the settled hook, both lines up and every machine joined, just before the drop
node render.mjs stills "$work" 4.95
ffmpeg -loglevel error -y -i "$work/t04.95.png" -q:v 3 poster.jpg
ffmpeg -loglevel error -y -i "$work/silent.mp4" -i music.m4a -map 0:v -map 1:a \
  -c:v copy -c:a copy -movflags +faststart yonder-demo.mp4

# readme gif: silent, 880 wide, 12 fps, rendered without the slow ambient drift so it
# stays under 5 mb
QUERY=lite node render.mjs frames "$work/lite.mp4" 12
ffmpeg -loglevel error -y -i "$work/lite.mp4" \
  -vf "scale=880:-1:flags=lanczos,palettegen=max_colors=96:stats_mode=diff" "$work/pal.png"
ffmpeg -loglevel error -y -i "$work/lite.mp4" -i "$work/pal.png" \
  -lavfi "scale=880:-1:flags=lanczos[x];[x][1:v]paletteuse=dither=none:diff_mode=rectangle" \
  hero.gif
ls -la yonder-demo.mp4 hero.gif poster.jpg
