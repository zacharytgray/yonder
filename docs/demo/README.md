# Demo video

The 28-second video at the top of the main README. One line for anyone with more than one
computer, then a coding agent on the laptop that works on the desktop's thesis repo and runs the
tests on the PC, a text from the phone to the agent on the Pi that checks the tests and a key,
and a rule added on the laptop that every machine's agents get. Then the tagline and how to set
it up.

| File | What it is |
|---|---|
| `yonder-demo.mp4` | 1920×1080, 30 fps, 27.7 s, with sound |
| `hero.gif` | the same cut, silent, 1280 wide, for the README |
| `poster.jpg` | the first frame: the hook |
| `brag-plan.md` | the storyboard with timestamps, the music and the accuracy notes |
| `share-copy.txt` | a caption to post with it |
| `index.html` | the composition. Every frame is a pure function of time: open it with `?t=12.5` to see one |
| `render.mjs` | drives headless Chrome over the devtools protocol and pipes frames into ffmpeg |
| `music.py` | synthesizes the soundtrack (soft felt piano, a light pad, a soft pulse, 92 BPM in D major, plain triads), stdlib only |
| `fleet.sh` | builds the fake fleet and prints the real output used on screen, plus `ls`, `layers`, `log` and `agents install` for the README |
| `build.sh` | runs all of it, about five minutes |
| `fonts/` | Fraunces, Instrument Sans and JetBrains Mono (latin woff2), each with its OFL license |

Styled with the Yonder brand ([`docs/brand.md`](../brand.md)). The hook and outro use the dusk
ground, and the scenes use paper. The logo and icon come from `docs/brand/`. Planned with the
brag skill ([latent-spaces/brag](https://github.com/latent-spaces/brag), the `/brag-slim`
workflow). Nothing from that repo is vendored here.

Every command and its output on screen is real, printed by `fleet.sh`. The coding agent's frame
around them (prompt box, tool-call bullets, replies) is drawn, with no product name. The fake
fleet has no second reachable machine, so for each scene the script makes the target machine
`self`, and yonder runs the command in-process through the same `_exec` code an ssh call
reaches. The thesis repo is a real git repo, the tests are 212 small `unittest` cases, the secret
comes from a stand-in `op` with a made-up value, and `hf` is a stand-in for `hf auth whoami`.
Every machine name is an example: Laptop, Desktop, PC and Pi on screen, and `laptop`, `desktop`,
`pc` and `pi` in the terminal. No machine is a hub. Addresses are in `100.64.0.x` and the user
is `me`. 1Password and the agent harnesses appear as plain text names only.

To rebuild, you need Node 22+, Google Chrome, ffmpeg and Python 3:

```sh
make build && docs/demo/fleet.sh     # optional: check the on-screen output
docs/demo/build.sh
```

`fleet.sh` prints a new job id and test time each run. The video uses the ones in `index.html`.
