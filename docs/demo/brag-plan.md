# Yonder demo: plan (v11)

Made with the brag skill (github.com/latent-spaces/brag, the `/brag-slim` workflow): rubric,
angle, storyboard, then build. The beat grid comes from the skill's
`scripts/analyze_music_cues.py`.

v1 to v8 grew to 41 seconds around moving slides between machines. v9 cut it to three asks
texted to one machine. v10 dropped the hub: the agent starts wherever you are. v11 keeps v10's
story and puts it to a real track, an upbeat funk groove, with far more motion. Every scene
change lands on a downbeat. Machine names are plain: Laptop, Desktop, Gaming PC and
Raspberry Pi. There is no bare "Pi" and no slides.

## Rubric

- **What it is:** a CLI that connects your own computers so the agent on any one of them can
  use all of them.
- **Who it's for:** anyone with more than one computer.
- **Best claim:** "Every computer you own, one agent."
- **Angle:** your machines already form a network; Yonder lets the agent use it. The four
  machines are one object the whole way through: dots on the horizon, then a strip of chips on
  paper, then dots again. Arcs between them pulse on the beat.
- **Hook:** "More than one computer? / Your agent uses them like one." over the build. Arcs join
  every machine through the riser, and the drop throws the four dots up into the strip.
- **Use cases shown:** work on another machine (the thesis repo on the desktop), a long job
  sent to the right machine (`--detach` to the gaming PC), a text from the phone to the agent
  on the Raspberry Pi, a key used by name, and one rule shared by every agent.
- **Real UI shown:** every command and its output comes from the branch's binary against
  `fleet.sh`'s fake fleet. The coding agent's frame is drawn, with no product name or logo.
- **Tone:** high energy and clean. Fast expo-out moves, no overshoot, no bounce, no flashing
  colors. Brand colors, Fraunces, Instrument Sans and JetBrains Mono only.

## Music

"prettyjohn1-no-copyright-music-498106.mp3" from Pixabay, under the Pixabay Content License
(free to use, no attribution required). It is 80.6 s; the repo keeps only the 28.4 s cut,
`music.m4a`, which `music.sh` makes from the full file.

The cue analysis gives 92.0 BPM (beat 0.652 s, bar 2.608 s) and a four-bar loop. The track's
shape: a riser into the groove at 3.0 s, two eight-bar phrases, a four-bar breakdown from
44.7 s, a riser bar, the drop at 55.14 s, eight bars, and a hard stop on the bar line at
76.01 s with a short reverb tail.

The cut runs 49.925 s to 78.325 s of the track: the last two breakdown bars as the build, the
drop, the final eight-bar phrase, the track's own stop, and 2.3 s of its tail, with a 0.4 s
fade at the very end where the tail is already below −55 dB. It is the only stretch that ends
on the song's real ending instead of a fade. Level: two-pass loudnorm to −16 LUFS, −1.5 dBTP.
In the cut, the first downbeat is t = 0, and onsets sit within ±13 ms of the grid.

## Storyboard (28.4 s)

Bars are 2.608 s. "b" is a beat inside the scene.

| # | Time | Bars | What happens | Text on screen |
|---|---|---|---|---|
| 1 | 0.00–5.22 | 0–1, the build | Dusk horizon, sun, stars. The camera pushes in slowly with parallax (sky, sun, land at different rates). Four hollow rings: Laptop, Desktop, Gaming PC, Raspberry Pi. Each pings on its beat of bar 0. Bar 1, the riser: line two lands word by word, then six arcs draw between every pair on quickening beats (2, 2.5, 3, 3.25, 3.5, 3.75); each machine lights as its first arc lands, and packets run the arcs. | "More than one computer?" / "Your agent uses them like one." |
| 2 | 5.22–13.04 | 2–4, the drop | On the drop: a flash and ring burst on every node and a camera punch. The paper opens from the horizon line like a slit, the four dots fly up and morph into the strip of chips, and the agent window rises in 3D. The laptop's coding agent: "The thesis repo is on my desktop. Pick up where I left off." is typed and sent on b2; `yonder run desktop -- 'git -C ~/thesis log --oneline -3'` spins, then three real commits land on b3 while an arc fires Laptop → Desktop. Reply on b4. "Run the tests on the gaming PC, it's faster." sent on b6; `yonder run gaming-pc --detach -- 'cd ~/thesis && python3 -m unittest -q'` prints the job id and the started line on b7, arc Laptop → Gaming PC, chip reads "tests running". Reply on b8. | "Ask on your laptop. It works where the work is." |
| 3 | 13.04–18.26 | 5–6 | A whip pan with motion blur behind an ink band. The "agent here" tag slides to Raspberry Pi. A phone texts the agent there: "Did the tests pass?" It runs `yonder jobs gaming-pc <id>` ("Ran 212 tests …", "OK"), arc Raspberry Pi → Gaming PC, chip "212 tests passed". "All 212 passed on the gaming PC." On b4: "Is my Hugging Face key working there?" `yonder run gaming-pc --secret HF_TOKEN -- hf auth whoami` prints "✓ Logged in, user: me". "Yes. From 1Password, by name." | "Text it from your phone." then "Keys by name. Never the value." |
| 4 | 18.26–23.47 | 7–8 | A vertical push with motion blur. The tag slides back to Laptop. Left, the coding agent: "Always run the tests before you commit." It edits `~/.local/share/yonder/agents/instructions.md` (+ "- Run the tests before you commit.") on b2 and replies. On b3 arcs fire from Laptop to the other three on sixteenths; each chip rolls to "rules in sync". Right, a terminal on the desktop, later: `tail -4` of the same file on b5, the new rule highlighted on b6. | "Add a rule once. Every agent knows the drill." |
| 5 | 23.47–28.40 | 9, then the stop | The paper folds back into the horizon, the chips fall back into dots, and the camera pulls back over the dusk. Arcs redraw on sixteenths. The lockup wipes in, the tagline lands word by word, "Tell your agent:" on b1, the setup line types to b2, the platforms on b2. At 26.08 s the music stops: one last ring burst and arc flash, then the packets stop and the frame holds in the tail. | "Every computer you own, one agent." / "Tell your agent:" / "> Set up Yonder for me: https://github.com/zacharytgray/yonder" / "macOS and Linux · Claude Code, Codex, Pi, Hermes · MIT" |

### Cuts against the beat grid

| Cut | Video time | Grid time | Track time | Beat |
|---|---|---|---|---|
| start | 0.000 | 0.000 | 49.925 | bar 0, beat 1 (build) |
| drop: hook → agent | 5.216 | 5.216 | 55.141 | bar 2, beat 1 (the drop) |
| agent → phone | 13.041 | 13.041 | 62.966 | bar 5, beat 1 |
| phone → rules | 18.257 | 18.257 | 68.182 | bar 7, beat 1 |
| rules → outro | 23.473 | 23.473 | 73.398 | bar 9, beat 1 |
| music stops | 26.082 | 26.082 | 76.007 | bar 10, beat 1 |
| end | 28.400 | | 78.325 | tail |

Whips and wipes are centered on the cut: the old scene leaves in the 0.24 s before it and the
new one settles in the 0.5 s after. Inside scenes, every send, output and arc sits on a beat,
an off-beat or a sixteenth.

## Motion and polish

- One easing family: expo-out for entrances, cubic-in for exits, cubic in-out for camera
  moves. No linear tweens except the character-by-character typing, which has a small human
  unevenness.
- Kinetic type: every caption and headline word rises out of its own mask, staggered 40–55 ms,
  and leaves the same way. Each caption is fully up for at least 0.3 s per word.
- Camera: slow push-ins per scene, a small punch on each command's Enter and output, parallax
  between the dot grid, the strip and the windows. Motion blur on the whips is a directional
  blur set from the frame's own velocity.
- The network: arcs pulse in width on every beat, a packet runs an arc on each beat (all of
  them on the downbeat), and the dashed track between chips steps forward a notch each beat.
  Everything that pulses stops when the music stops.
- Chips: status lines roll up when they change, a chip pops 3.5 % when a packet arrives, and the
  "agent here" tag slides between machines.
- Safe margins: all text is inside 150 px of the sides and 90 px of the bottom. Captions are 54 px
  Fraunces, readable at phone size.
- First frame: the dusk, the four rings and "More than one computer?", already set.
  Poster: 4.95 s, the settled hook with every machine joined, just before the drop.

## Accuracy notes

- `fleet.sh` builds the fake fleet and prints every line used on screen. A machine runs a
  command in-process when it is `self`, through the same `_exec` code an ssh call reaches. So
  for each scene the script makes the target machine `self` and runs from a home of its own.
  The `yonder run` and `yonder jobs` output is the binary's own.
- The thesis repo is a real git repo with three commits at fixed dates, so the hashes are the
  same every run.
- The gaming PC's copy has 212 small `unittest` tests that sleep a little; the time in "Ran 212
  tests" is whatever that run took. The job id and time are the ones the run printed;
  `fleet.sh` prints new ones each time.
- The coding agent's frame, its tool-call bullets, spinner and replies are drawn. The edit line
  in scene 4 is a file edit, not a command. The right pane's `tail` reads the store file the
  fake fleet wrote, which is what sync carries to every machine and `agents link` renders into
  each harness's file (`fleet.sh` prints the rendered `AGENTS.md` too).
- Stand-ins: the secret comes from a stand-in `op` that answers `op inject` with a made-up
  value. `hf` is a stand-in that prints what `hf auth whoami` prints in huggingface_hub 2.x
  when `HF_TOKEN` is set. The key's value never appears.
- The phone agent is Hermes on the Raspberry Pi, shown only as "agent". The runs pass `--as`
  with the harness, which is a plain name. The phone shows only the replies; the terminal
  beside it shows what the agent ran.
- Every name is an example. On screen the machines are Laptop, Desktop, Gaming PC and Raspberry
  Pi; in the terminal they are `laptop`, `desktop`, `gaming-pc` and `raspberry-pi`. The user is
  `me`, the addresses are `100.64.0.x`, and 1Password appears as a plain name.
- The setup line is the README's own: tell your agent "Set up Yonder for me" with the repo link.
