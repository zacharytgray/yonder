# Yonder demo: plan (v10)

Made with the brag skill (github.com/latent-spaces/brag, the `/brag-slim` workflow).

v1 to v8 grew to 41 seconds around one story: moving slides from the Mac mini to the laptop.
v9 cut it to 21.6 seconds of three asks, all texted to an agent on the Mac mini. v10 starts over
again. No machine is the hub: the agent starts wherever you are. The video opens in a coding
agent on the laptop, picks up from the phone, and shows that every agent shares the same rules.
The music is new: a plain D major progression on a soft piano.

## Rubric

- **What it is:** a CLI that connects your own computers so the agent on any one of them can
  use all of them.
- **Who it's for:** anyone with more than one computer.
- **Best claim:** "Every computer you own, one agent."
- **Hook:** "More than one computer? / Your agent uses them like one." on the dusk horizon. Four
  hollow rings, then arcs join every machine to every other, and they light.
- **Use cases shown:** work on another machine (the thesis repo on the desktop), send a job to
  the right machine (tests on the PC), check on it from anywhere (a text to the agent on the Pi),
  keys by name (a Hugging Face key from 1Password), and the same rules for every agent.
- **Real UI shown:** every command and its output is real, from the branch's binary against
  `fleet.sh`'s fake fleet. The coding agent's frame is drawn: a prompt box, compact tool-call
  lines and short replies, with no product name or logo. See the accuracy notes.
- **Tone:** calm. Gentle fades and small rises, no overshoot, no flashing, a steady cursor.

Every name is an example. On screen the machines are Laptop, Desktop, PC and Pi; in the terminal
they are `laptop`, `desktop`, `pc` and `pi`. The user is `me`, the addresses are `100.64.0.x`,
and 1Password appears as a plain name.

## Storyboard (27.7 s)

The music is 92 BPM, so a beat is 0.652 s and a bar 2.609 s. Every cut lands on a beat; the
phone scene, the sync scene and the outro start on a downbeat.

| # | Time | Ground | What happens | Text on screen |
|---|---|---|---|---|
| 1 | 0.0–3.3 | dusk | Horizon and sun. Laptop, Desktop, PC and Pi are hollow rings. At 1.25 s arcs join every pair, and at 1.9 s all four light. The text fades just before the cut. | "More than one computer?" / "Your agent uses them like one." |
| 2 | 3.3–10.4 | paper | A coding agent on the laptop. The ask is typed into its prompt box: "The thesis repo is on my desktop. Pick up where I left off." It runs `yonder run desktop -- 'git -C ~/thesis log --oneline -3'` (three real commits) and replies "You stopped at the chapter 4 results table. I'll pick it up there." Then "Run the tests on the PC, it's faster.": `yonder run pc --detach -- 'cd ~/thesis && python3 -m unittest -q'` prints the job id and "yonder: started on pc …". Reply: "The tests are running on the PC. Back to chapter 4." The strip shows Laptop as "agent here", with arcs to Desktop and then PC. | "Ask on your laptop. It works where the work is." |
| 3 | 10.4–18.3 | paper | Later, the phone, texting the agent that runs on the Pi. "How are the tests doing?" It runs `yonder jobs pc` (the job, exited 0) and `yonder jobs pc <id>` ("Ran 212 tests in 13.526s", "OK"). Reply: "All 212 passed on the PC." Then "Is my Hugging Face key working there?": `yonder run pc --secret HF_TOKEN -- hf auth whoami` prints "✓ Logged in, user: me". Reply: "Yes. It came from 1Password by name." | "Text it from your phone. Start anywhere." then "Keys by name. The value stays out of output and logs." |
| 4 | 18.3–23.5 | paper | Left, the laptop's coding agent: "Always run the tests before you commit." It edits `~/.local/share/yonder/agents/instructions.md` (+ "- Run the tests before you commit.") and replies "Added to your shared rules. Every machine gets them." Arcs go from Laptop to the other three, each chip reading "rules in sync". Right, a terminal on the desktop, later: `tail -4` of the same file shows the house rules with the new one highlighted. | "Your setup follows you. Every agent knows the drill." |
| 5 | 23.5–27.7 | dusk | The paper dips to night, then the horizon fades up with all four machines lit and joined. Lockup, tagline, then the setup line and the platforms. The final D chord rings out. | "Every computer you own, one agent." / "Tell your agent:" / "> Set up Yonder for me: https://github.com/zacharytgray/yonder" / "macOS and Linux · Claude Code, Codex, Pi, Hermes · MIT" |

## Music

`music.py`, synthesized from scratch with the standard library. 92 BPM in D major, plain
triads only, one chord a bar: D, A, Bm, G, D, A, Bm, G (I, V, vi, IV twice), then G and A a half
bar each (IV, V), and D held under the outro. No sevenths, suspensions or borrowed chords.

- **Felt piano:** a few slightly stretched partials that fade faster as they go up, two strings
  a hair apart on the lowest two, a soft 8 ms attack and a damper at the end, low-passed at
  3.8 kHz. Left hand: the root on 1 and the fifth on 3. Right hand: the close triad on 1, rolled
  a little, and softer on 3.
- **Top line:** from bar 1, half and quarter notes between A4 and F♯5, all in D major and mostly
  chord tones: C♯ E | D F♯ E | D B | A D F♯ | E C♯ E | F♯ E D | B D | D C♯, landing on D.
- **Pad:** the triad as detuned sines with a soft octave, swelling in and out under each chord,
  between 80 Hz and 1.2 kHz.
- **Pulse:** from the first scene, a soft felt kick on 1 and 3 and a quiet brush on 2 and 4.
- **Room:** a small warm reverb on the piano and pad, different left and right.

No plucks, risers, whooshes or busy bass. `build.sh` brings it to −16 LUFS.

## Accuracy notes

- `fleet.sh` builds the fake fleet and prints every line used on screen. A machine runs a
  command in-process when it is `self`, through the same `_exec` code an ssh call reaches. So
  for each scene the script makes the target machine `self` and runs from a home of its own.
  The `yonder run` and `yonder jobs` output is the binary's own.
- The thesis repo is a real git repo with three commits at fixed dates, so the hashes are the
  same every run.
- The PC's copy has 212 small `unittest` tests that sleep a little; the time in "Ran 212 tests"
  is whatever that run took. `yonder jobs` read the log after the job exited. The job id and
  times are the ones that run printed; `fleet.sh` prints new ones each time.
- The coding agent's frame, its tool-call bullets and its replies are drawn. The edit line in
  scene 4 is a file edit, not a command. The right pane's `tail` reads the store file the fake
  fleet wrote, which is what sync carries to every machine and `agents link` renders into each
  harness's file (`fleet.sh` prints the rendered `AGENTS.md` too).
- The secret comes from a stand-in `op` that answers `op inject` with a made-up value. `hf` is a
  stand-in that prints what `hf auth whoami` prints in huggingface_hub 2.x when `HF_TOKEN` is
  set. The key's value never appears.
- The phone agent is Hermes on the Pi, shown only as "agent". The runs pass `--as` with the
  harness, which is a plain name.
- The setup line is the README's own: tell your agent "Set up Yonder for me" with the repo link.
