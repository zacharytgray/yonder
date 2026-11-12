# Demo video

The one-minute video at the top of the main README.

| File | What it is |
|---|---|
| `yonder-demo.mp4` | 1920×1080, 30 fps, 60 s, with sound |
| `hero.gif` | the same cut, silent, 1280 wide, for the README |
| `poster.jpg` | the title card, also baked in as the mp4's first frame |
| `brag-plan.md` | the storyboard |
| `share-copy.txt` | a caption to post with it |
| `index.html` | the composition. Every frame is a pure function of time: open it with `?t=12.5` to see one |
| `render.mjs` | drives headless Chrome over the devtools protocol and pipes frames into ffmpeg |
| `music.py` | synthesizes the soundtrack, stdlib only |
| `build.sh` | runs all of it, about ten minutes |
| `fonts/` | Fraunces, Instrument Sans and JetBrains Mono (latin woff2), each with its OFL license |

Styled with the Yonder brand ([`docs/brand.md`](../brand.md)), dark variant; the logo and
icon come from `docs/brand/`. Planned with the brag skill ([latent-spaces/brag](https://github.com/latent-spaces/brag),
the `/brag-slim` workflow). Nothing from that repo is vendored here.

The `yonder ls` and `yonder log` tables are real output from the binary, run against a fake
fleet (`YONDER_HOME` with hand-written machine records and a crafted `YONDER_TAILSCALE_JSON`).
The `yonder run` output is staged. Every name is an example.

To rebuild, you need Node 22+, Google Chrome, ffmpeg and Python 3:

```sh
docs/demo/build.sh
```
