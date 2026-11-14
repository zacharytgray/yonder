#!/bin/sh
# makes music.m4a, the soundtrack build.sh uses, from the full track.
# the track: "prettyjohn1-no-copyright-music-498106.mp3" from pixabay (pixabay content
# license, free to use, no attribution needed). only the trimmed cut is in the repo.
# usage: docs/demo/music.sh path/to/prettyjohn1-no-copyright-music-498106.mp3
# needs ffmpeg and python3.
set -eu
cd "$(dirname "$0")"
src=$1
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# 92 bpm. the cut starts on the downbeat two bars before the drop (49.925 s), runs the
# drop's eight bars to the track's own stop at 76.007 s, and keeps 2.3 s of its tail.
start=49.925
dur=28.4
ffmpeg -loglevel error -y -i "$src" -ar 44100 "$work/full.wav"
ffmpeg -loglevel error -y -ss "$start" -t "$dur" -i "$work/full.wav" \
  -af "afade=t=in:d=0.008,afade=t=out:st=$(echo "$dur - 0.4" | bc):d=0.4" "$work/cut.wav"

# two-pass loudnorm to -16 LUFS, -1.5 dBTP
m=$(ffmpeg -hide_banner -i "$work/cut.wav" -af loudnorm=I=-16:TP=-1.5:LRA=11:print_format=json -f null - 2>&1 | sed -n '/^{/,/^}/p')
get() { printf '%s' "$m" | python3 -I -c "import json,sys; print(json.load(sys.stdin)['$1'])"; }
ffmpeg -loglevel error -y -i "$work/cut.wav" -af "loudnorm=I=-16:TP=-1.5:LRA=11:linear=true:measured_I=$(get input_i):measured_TP=$(get input_tp):measured_LRA=$(get input_lra):measured_thresh=$(get input_thresh):offset=$(get target_offset)" -ar 44100 "$work/norm.wav"
ffmpeg -loglevel error -y -i "$work/norm.wav" -c:a aac -b:a 192k -movflags +faststart music.m4a
ls -la music.m4a
