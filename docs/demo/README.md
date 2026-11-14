# Demo video

The 22-second video at the top of the main README. The problem in one line, then three
everyday asks: fix the Pi's full disk, run a training job on the PC, and use a key by name. Then
the tagline and how to set it up.

| File | What it is |
|---|---|
| `yonder-demo.mp4` | 1920×1080, 30 fps, 21.6 s, with sound |
| `hero.gif` | the same cut, silent, 1280 wide, for the README |
| `poster.jpg` | the first frame: the hook |
| `brag-plan.md` | the storyboard with timestamps, the music and the accuracy notes |
| `share-copy.txt` | a caption to post with it |
| `index.html` | the composition. Every frame is a pure function of time: open it with `?t=12.5` to see one |
| `render.mjs` | drives headless Chrome over the devtools protocol and pipes frames into ffmpeg |
| `music.py` | synthesizes the soundtrack (electric piano, low pad, a soft felt pulse, 84 BPM in F), stdlib only |
| `fleet.sh` | builds the fake fleet and prints the real output used on screen, plus `ls`, `layers`, the `CLAUDE.md` block, `log` and `agents install` for the README |
| `build.sh` | runs all of it, about four minutes |
| `fonts/` | Fraunces, Instrument Sans and JetBrains Mono (latin woff2), each with its OFL license |

Styled with the Yonder brand ([`docs/brand.md`](../brand.md)). The hook and outro use the dusk
ground, and the scenes use paper. The logo and icon come from `docs/brand/`. Planned with the
brag skill ([latent-spaces/brag](https://github.com/latent-spaces/brag), the `/brag-slim`
workflow). Nothing from that repo is vendored here.

Every line in the terminal is real output from the binary, printed by `fleet.sh`. The fake fleet
has no second reachable machine, so for each scene the script makes the target machine `self`,
and yonder runs the command in-process through the same `_exec` code an ssh call reaches. The
Pi's cache files are real, `train.py` is a small script that prints a loss each epoch, and the
secret comes from a stand-in `op` with a made-up value, which the scrubber prints as
`[HF_TOKEN]`. Every machine name is an example: Laptop, Pi, Mac mini and PC on screen, and
`laptop`, `pi`, `mac-mini` and `pc` in the terminal. Addresses are in `100.64.0.x` and the user
is `me`. 1Password and the agent harnesses appear as plain text names only.

To rebuild, you need Node 22+, Google Chrome, ffmpeg and Python 3:

```sh
make build && docs/demo/fleet.sh     # optional: check the on-screen output
docs/demo/build.sh
```

`fleet.sh` prints a new job id each run. The video uses the one in `index.html`.
