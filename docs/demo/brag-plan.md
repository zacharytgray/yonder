# Yonder demo: plan (v9)

Made with the brag skill (github.com/latent-spaces/brag, the `/brag-slim` workflow).

v1 to v8 grew to 41 seconds around one story: moving slides from the Mac mini to the laptop.
v9 starts over. The slides are gone. The video is 21.6 seconds: the problem in one line, three
everyday asks with the real commands, then the tagline and how to set it up. The music is new
too: a slow electric piano instead of the whistle, claps and shaker.

## Rubric

- **What it is:** a CLI that connects your own computers so the agent on any one of them can
  use all of them.
- **Who it's for:** people whose work is spread over a laptop, an always-on box, a GPU box and a
  Pi, and whose agent only knows the machine it runs on.
- **Best claim:** "Every computer you own, one agent."
- **Hook:** "Your work is on four computers. Your agent knows one." on the dusk horizon. Only the
  Mac mini is lit; arcs then reach the other three.
- **Use cases shown:** fix another machine (the Pi's full disk), send a job to the right machine
  (training on the PC, checked later), and keys by name (a Hugging Face token from 1Password).
  These are items 2, 3 and 6 of the shared list. The README leads with others.
- **Real UI shown:** every line in the terminal is real output from the branch's binary against
  `fleet.sh`'s fake fleet. See the accuracy notes.
- **Tone:** calm. Gentle fades and small rises, no overshoot, no flashing, a steady cursor.

Every name is an example. On screen the machines are Laptop, Pi, Mac mini and PC; in the
terminal they are `laptop`, `pi`, `mac-mini` and `pc`. The user is `me`, the addresses are
`100.64.0.x`, and 1Password appears as a plain name.

## Storyboard (21.6 s)

The music is 84 BPM, so a bar is 2.857 s. The first scene starts on bar 1 and the outro on
bar 6, where the last chord lands.

| # | Time | Ground | What happens | Text on screen |
|---|---|---|---|---|
| 1 | 0.0–2.9 | dusk | Horizon and sun. Laptop, Pi and PC are hollow rings; the Mac mini is lit, tagged "your agent". At 1.5 s arcs reach from it to the other three, and they light. The text fades just before the cut. | "Your work is on four computers." / "Your agent knows one." |
| 2 | 2.9–7.9 | paper | Phone: "The Pi's disk is full, can you clean it up?" Terminal on mac-mini: `yonder run pi -- 'du -sh .cache/* \| sort -h'` lists fontconfig 12K, thumbnails 46M, pip 2.1G (highlighted). Then `yonder run pi -- 'rm -rf .cache/pip && du -sh .cache'` prints 46M. The link runs Mac mini → Pi, whose chip reads "2.1 GB freed". Reply: "Done. I cleared 2.1 GB of old pip downloads." | "Fix another machine." |
| 3 | 7.9–12.9 | paper | Phone: "Run the training script on the PC." `yonder run pc --detach -- python3 train.py` prints the job id and "yonder: started on pc; output: yonder jobs pc …". Then `yonder jobs pc <id>` prints epochs 1 to 3. The PC chip reads "train.py running". Reply: "It's running there. Epoch 3 of 20 so far." | "Send a job to the right machine." |
| 4 | 12.9–17.1 | paper | Phone: "Use my Hugging Face token on the PC." `yonder run pc --secret HF_TOKEN -- 'echo $HF_TOKEN'` prints `[HF_TOKEN]`. The PC chip reads "HF_TOKEN, by name". Reply: "Done. It came from 1Password by name." | "Keys by name. The value stays out of output and logs." |
| 5 | 17.1–21.6 | dusk | The paper dips to night, then the horizon fades up with all four machines lit. Lockup, tagline, then the setup line and the platforms. The final chord rings out. | "Every computer you own, one agent." / "Tell your agent:" / "> Set up Yonder for me: https://github.com/zacharytgray/yonder" / "macOS and Linux · Claude Code, Codex, Pi, Hermes · MIT" |

The phone thread carries over between scenes, so each reply stays readable into the next ask.

## Music

`music.py`, synthesized from scratch with the standard library. 84 BPM in F major, one chord a
bar: Fmaj9, Am7, B♭maj9, C9sus4, Dm9, then B♭maj9 and C9sus4 a half bar each, and Fmaj9 held
under the outro.

- **Electric piano:** two-operator FM at ratio 1, so the attack is a little bright and then
  mellows, plus a quiet tine partial that dies in a tenth of a second and a slow tremolo. The
  chord is rolled on 1, with a lighter top three notes on the and of 2. Voicings stay between F3
  and A4.
- **Low pad:** root, fifth and third, three slightly detuned sines each, swelling in and out
  under each chord, low-passed at 900 Hz and high-passed at 70 Hz.
- **Pulse:** a soft felt thump on 1 and 3, about 52 Hz, from the first scene on.
- **Room:** a small dark reverb on the piano and pad, different left and right.

No lead melody, no plucks, no risers or swishes. `build.sh` brings it to −16 LUFS.

## Accuracy notes

- `fleet.sh` builds the fake fleet and prints every line used on screen. A machine runs a
  command in-process when it is `self`, through the same `_exec` code an ssh call reaches. So
  for each scene the script makes the target machine `self` and runs from a home of its own.
  The `yonder run`, `yonder jobs` and scrubber output is the binary's own.
- The Pi's `.cache` holds real files of those sizes, and the commands really run `du` and `rm`.
  `du` prints a tab; the video shows it as spaces.
- `train.py` is a ten-line script that prints a made-up loss each epoch and sleeps. `yonder jobs`
  read its log three seconds after it started. The job id is the one that run printed.
- The secret comes from a stand-in `op` that answers `op inject` with a made-up value. The value
  never appears: the scrubber prints `[HF_TOKEN]` for it, which is what Yonder does with any
  exact value.
- The terminal title says hermes, and the runs pass `--as hermes`. Hermes is a plain name.
- The setup line is the README's own: tell your agent "Set up Yonder for me" with the repo link.
