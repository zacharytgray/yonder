# Demo video

The 33-second video at the top of the main README.

| File | What it is |
|---|---|
| `yonder-demo.mp4` | 1920×1080, 30 fps, 33 s, with sound |
| `hero.gif` | the same cut, silent, 1280 wide, for the README |
| `poster.jpg` | the hook frame, also baked in as the mp4's first frame |
| `brag-plan.md` | the storyboard |
| `share-copy.txt` | a caption to post with it |
| `index.html` | the composition. Every frame is a pure function of time: open it with `?t=12.5` to see one |
| `render.mjs` | drives headless Chrome over the devtools protocol and pipes frames into ffmpeg |
| `music.py` | synthesizes the soundtrack (whistle, held bass roots, electric piano, kick, claps, shaker), stdlib only |
| `fleet.sh` | builds the fake fleet and prints the real `ls`, `log` and `agents install` output used on screen |
| `build.sh` | runs all of it, about six minutes |
| `fonts/` | Fraunces, Instrument Sans and JetBrains Mono (latin woff2), each with its OFL license |

Styled with the Yonder brand ([`docs/brand.md`](../brand.md)). The hook and outro use the dusk
ground, and the rest uses paper. The logo and icon come from `docs/brand/`. Planned with the
brag skill ([latent-spaces/brag](https://github.com/latent-spaces/brag), the `/brag-slim`
workflow). Nothing from that repo is vendored here.

The `yonder ls`, `yonder log` and `yonder agents install` output is real, from `fleet.sh`. The
`yonder cp`, `yonder run` and `yonder secret setup` output is staged from the strings the code
prints. Every machine name is an example: Laptop, Mac Mini, PC and Raspberry Pi on screen, and
`laptop`, `mac-mini`, `pc` and `raspberry-pi` in the terminal. 1Password, Bitwarden, Homebrew and the agent harnesses appear as
plain text names only.

To rebuild, you need Node 22+, Google Chrome, ffmpeg and Python 3:

```sh
make build && docs/demo/fleet.sh     # optional: check the on-screen tables
docs/demo/build.sh
```
