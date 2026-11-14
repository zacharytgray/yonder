# Yonder demo: plan (v8)

Made with the brag skill (github.com/latent-spaces/brag, the `/brag-slim` workflow).

v1 ran a minute and showed one fix. v2 cut it to 33 seconds and added the other strengths. v3
swapped the banjo for soft electric piano. v4 changed the story to an everyday rescue: a file
needed on one machine, right now, from another, and gave the machines everyday names. v5 to v7
only changed the music. The old plucked bass line, root and fifth alternating under the piano,
was the sound that grated. v7 holds one soft root a bar under a plain I–IV–V–I.

v8 keeps v7's look and sound and re-cuts the story for the new line, "Every computer you own,
one agent." It runs 41 seconds: the same progression one more time, so the build and the outro
land where they did in v7, two bars later. It shows v0.3.1: `yonder ls` without the models
column, the layers, and the briefing every agent loads. The GPU job, `secret setup` and the full
`yonder log` are gone.

## Rubric

- **What it is:** a CLI that connects your own computers so the agent on any one of them can
  use all of them. To your agent, every machine works like one computer.
- **Who it's for:** people whose work is spread over a laptop, an always-on box and a GPU box,
  and whose agent only knows the machine it runs on.
- **What sets it apart:** one agent, every computer. It moves your slides from the Mac Mini to
  the laptop on request, finds your projects on other machines, and every agent loads the same
  instructions and map of your machines.
- **Best claim:** "Every computer you own, one agent."
- **Visual hook:** the tagline lands on a sunset horizon. Laptop, Mac Mini, PC and Raspberry Pi
  light up along it within the first three seconds; the Raspberry Pi stays a hollow ring, asleep.
- **Real UI shown:** `yonder ls`, `yonder layers`, the laptop's `CLAUDE.md` block, the
  `yonder log` row and `yonder agents install` are real output from the binary against a fake
  fleet (`fleet.sh`). The `yonder cp` line is staged from `copySummary` in `cp.go`, the job and
  `ls` output of `yonder run` from what those commands print, and `yonder version` from the
  version string, since the fake fleet has no second reachable machine.
- **Tone:** `default`, bright and playful. Calm words, quick cuts.
- **Share caption:** see `share-copy.txt`.

Every name is an example. On screen the machines are Laptop, Mac Mini, PC and Raspberry Pi, and
in the terminal their hostnames are `laptop`, `mac-mini`, `pc` and `raspberry-pi`. The roles are
laptop, server (where the agent lives), desktop (the RTX 4090) and other (asleep). The tailnet is
`example.ts.net`, the addresses are `100.64.0.x` and the user is `me`. 1Password, Bitwarden,
Homebrew, Claude Code, Codex, Pi and Hermes appear as plain text names only, with no logos.

## Accuracy notes

- `yonder ls` has no MODELS column. Its last line, `layers: secrets on, agents on (yonder
  layers)`, is what it prints with both layers on.
- `yonder layers` prints "set just now by mac-mini" because `fleet.sh` turns the layers on with
  `yonder enable` right before. That is the real text.
- The `CLAUDE.md` block is what `yonder agents link` writes on the laptop: the begin marker, then
  the briefing's generated machine list. The laptop is "this machine" there, which shows the same
  map reaching another computer.
- "✓ in sync" on the chips is a picture of the agents layer reaching each online machine. The
  sleeping Raspberry Pi gets it "at its next sync", which is what `yonder enable` says.
- Secrets wording follows the README's "What secrets protect". A printed secret shows as
  `[HF_TOKEN]`, which is what the scrubber does with an exact value, and the log row holds the
  name, not the value. There's no "never seen" claim.
- "No server" and "No daemon" follow the README: sync is peer to peer over SSH, and nothing runs
  until a command calls it.
- Homebrew needs `brew trust` before `brew install`, so both lines are shown, in the install
  scene and the outro.

## Identity

The brand from `docs/brand.md`. There are two grounds, and the cuts switch between them.

- **Dusk** (hook, outro): night `#1A1828` to `#2E2440` at the horizon, a cream horizon line, a
  flat `#F08A55` sun on it right of center with three reflection bars, low ridges, one lantern
  `#E3A23A` in the sky. Machines sit on the line as prairie `#9FBF8C` dots with mono names.
- **Paper** (everything else): flat `#F6F0E4`. Ink `#26223D` terminal panels with cream text.
  Chips and pills on `#FBF7EF`. Ember `#D2602F` for the live link and big caption accents,
  `#A3431A` for small orange text. Lantern for "down", prairie for online and in sync.

Fraunces (soft, wonky) for captions and titles, Instrument Sans for UI, JetBrains Mono for
commands and machine names. All are bundled in `fonts/`.

## Music

120 BPM in G, so a beat is 0.5 s and a bar 2 s. G C D G, one chord a bar, five times, then a
final G. Every whistle note fits it as written. The parts are v7's, unchanged; only the
arrangement is one pass longer and the effects follow the new cues. Everything is synthesized in
`music.py`:

- a whistled hook over the title, a short answer at 14 s, and the hook again at the end (38 s)
- a soft sine upright bass, one held root a bar (G2, C2, D2, G2), low-passed at 350 Hz and
  sitting under the mix
- soft low-passed electric piano chords on 2 and 4
- a light kick on 1 and 3, handclaps on 2 and 4, and an offbeat shaker; four on the floor and
  sixteenth shakers in the build (bars 17 and 18, 34–38 s)
- effects: a quiet tuned pluck on each pop, key ticks, and short swishes on cuts

`build.sh` brings it to −16 LUFS.

## Storyboard (20.5 bars, 41.0 s)

| # | Time | Ground | Scene | Text on screen |
|---|---|---|---|---|
| 1 | 0.0–4.0 | dusk | Horizon draws, sun sits. The tagline pops in two halves. Mac Mini, Laptop, PC light up on the line on beats; Raspberry Pi is a hollow ring, "asleep · seen 2h ago". Arcs run from the Mac Mini ("your agent") to each. | "Every computer you own," / "one agent." |
| 2 | 4.0–8.0 | paper | The phone pops in: "Shoot, I forgot to move my slides to my laptop. Can you send them over?" The terminal on mac-mini runs `yonder ls`. Four rows land on beats and the chips light; raspberry-pi reads "offline 2h ago". | "Your agent sees every computer you own." |
| 3 | 8.0–12.0 | paper | `ls ~/Documents/*.key` finds Talk.key. `yonder cp ~/Documents/Talk.key laptop:~/Desktop/` runs, an ember link runs Mac Mini → Laptop, and the Laptop chip shows "Desktop/Talk.key". Reply: "Done. Talk.key is on your laptop's Desktop." | "It finds your slides and sends them over." |
| 4 | 12.0–16.0 | paper | "also, where did I leave my thesis code?" `yonder run pc -- ls ~/GitHub` lists dotfiles, thesis, website; the link runs to the PC, whose chip shows "~/GitHub/thesis". Reply: "On the PC, in ~/GitHub/thesis." | "Work from anywhere." → "It finds your projects on any machine." |
| 5 | 16.0–19.0 | paper | The phone slides off, and the terminal widens. `yonder layers` shows connect, secrets and agents on. The chips tick "✓ in sync"; the Raspberry Pi gets it at its next sync. | "Context stays in sync." |
| 6 | 19.0–24.0 | paper | On the laptop, `head -8 ~/.claude/CLAUDE.md` shows the yonder block: the briefing and its machine list, generated from the records. | "Your instructions, skills and machine map, on every computer." |
| 7 | 24.0–30.0 | paper | Pills pop: "1Password", "Bitwarden". `yonder run pc --secret HF_TOKEN -- 'echo $HF_TOKEN'` prints `[HF_TOKEN]`, popping in ember. `yonder log \| tail -1` shows the row with the secret's name. | "Shared secrets, kept safe." → "Agents use names. Values stay out of commands, logs and output." |
| 8 | 30.0–34.0 | paper | You, on the laptop: `brew trust`, `brew install`, `yonder version`. Pills pop: "One binary", "No server", "No daemon". | "Simple and lightweight. One small program." |
| 9 | 34.0–38.0 | paper | `yonder agents install`. Pills pop: Claude Code, Codex, Pi, Hermes. The build. | "Works in Claude Code, Codex, Pi and Hermes." |
| 10 | 38.0–41.0 | dusk | Back on the horizon with all four machines. Lockup, tagline, the two brew lines and the repo. The hook comes back and the final chord rings. | "Every computer you own, one agent." / github.com/zacharytgray/yonder |

Cuts land on bar or half-bar lines, and pops on beats or half beats. Each caption holds at
least 0.3 s per word once it has settled. Transitions go out, then in, with no crossfade
between two busy layouts. Switching between dusk and paper is a hard cut on the bar.
