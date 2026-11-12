# Yonder demo: plan (final, v7)

Made with the brag skill (github.com/latent-spaces/brag, the `/brag-slim` workflow).

v1 ran a minute and showed one fix. v2 cut it to 33 seconds and added the other strengths. v3
swapped the banjo for soft electric piano. v4 changed the story to an everyday rescue: a file
needed on one machine, right now, from another, and gave the machines everyday names. v5 to v7
only changed the music. The old plucked bass line, root and fifth alternating under the piano,
was the sound that grated. v7 holds one soft root a bar under a plain I–IV–V–I. The picture is
v4's.

## Rubric

- **What it is:** a CLI that connects your own machines so the agent on one can use all of them.
- **Who it's for:** people with a laptop, an always-on box and a GPU box, who talk to an agent.
- **What sets it apart:** one agent, every machine. It moves a file from the Mac Mini to the
  laptop on request, and it picks the machine that has what a job needs.
- **Best claim:** "Text one agent. Fix any machine."
- **Visual hook:** the tagline lands on a sunset horizon. Laptop, Mac Mini, PC and Raspberry Pi
  light up along it within the first three seconds; the Raspberry Pi stays a hollow ring, asleep.
- **Real UI shown:** `yonder ls`, `yonder log` (including the `cp` row) and
  `yonder agents install` are real output from the binary against a fake fleet (`fleet.sh`).
  The `yonder cp`, `yonder run` and `yonder secret setup` lines are staged from the exact
  strings the code prints (`copySummary` in `cp.go` for the copy), since the fake fleet has no
  second reachable machine.
- **Tone:** `default`, bright and playful. Calm words, quick cuts.
- **Share caption:** see `share-copy.txt`.

Every name is an example. On screen the machines are Laptop, Mac Mini, PC and Raspberry Pi, and
in the terminal their hostnames are `laptop`, `mac-mini`, `pc` and `raspberry-pi`. The roles are
laptop, server (where the agent lives), desktop (the RTX 4090) and other (asleep). The tailnet is
`example.ts.net` and the user is `me`. The vault
is Yonder's default, "Yonder Agents". 1Password, Bitwarden, Homebrew, Claude Code, Codex, Pi and
Hermes appear as plain text names only, with no logos.

## Accuracy notes

- Secrets wording follows the README's "What secrets protect". The caption says a printed
  secret shows up as `[HF_TOKEN]`, which is what the scrubber does with an exact value. There's
  no "never seen" claim.
- "One approval sets up your machines" is the hands-free 1Password flow: one Touch ID or
  password approval, and a read-only service account for each other online machine. The
  machine you sit at keeps Touch ID, and the sleeping Raspberry Pi is skipped with its rerun
  line, which is what the code prints.
- Homebrew needs `brew trust` before `brew install`, so both lines are shown.

## Identity

The brand from `docs/brand.md`. There are two grounds, and the cuts switch between them.

- **Dusk** (hook, outro): night `#1A1828` to `#2E2440` at the horizon, a cream horizon line, a
  flat `#F08A55` sun on it right of center with three reflection bars, low ridges, one lantern
  `#E3A23A` in the sky. Machines sit on the line as prairie `#9FBF8C` dots with mono names.
- **Paper** (everything else): flat `#F6F0E4`. Ink `#26223D` terminal panels with cream text.
  Chips and pills on `#FBF7EF`. Ember `#D2602F` for the live link and big caption accents,
  `#A3431A` for small orange text. Lantern for "down", prairie for online.

Fraunces (soft, wonky) for captions and titles, Instrument Sans for UI, JetBrains Mono for
commands and machine names. All are bundled in `fonts/`.

## Music

120 BPM in G, so a beat is 0.5 s and a bar 2 s. G C D G, one chord a bar, four times, then a final
G. Every whistle note fits it as written. Everything is synthesized in `music.py`:

- a whistled hook over the title, a short answer at 14 s, and the hook again at the end
- a soft sine upright bass, one held root a bar (G2, C2, D2, G2), low-passed at 350 Hz and
  sitting under the mix
- soft low-passed electric piano chords on 2 and 4
- a light kick on 1 and 3, handclaps on 2 and 4, and an offbeat shaker
- effects: a quiet tuned pluck on each pop, key ticks, and short swishes on cuts

`build.sh` brings it to −16 LUFS.

## Storyboard (16.5 bars, 33.0 s)

| # | Time | Ground | Scene | Text on screen |
|---|---|---|---|---|
| 1 | 0.0–4.0 | dusk | Horizon draws, sun sits. The tagline pops in two halves. Mac Mini, Laptop, PC light up on the line on beats; Raspberry Pi is a hollow ring, "asleep · seen 2h ago". Arcs run from the Mac Mini ("your agent") to each. | "Text one agent." / "Fix any machine." |
| 2 | 4.0–8.0 | paper | The phone pops in: "I'm about to give a presentation but I forgot to download it on my laptop! It's on my Mac Mini. Can you send it over?" The terminal on mac-mini runs `yonder ls`. Four rows land on beats and the chips light; raspberry-pi reads "offline 2h ago". | "One agent sees every machine." |
| 3 | 8.0–12.0 | paper | `ls ~/Documents/*.key` finds Talk.key. `yonder cp ~/Documents/Talk.key laptop:~/Desktop/` runs, an ember link runs Mac Mini → Laptop, and the Laptop chip shows "Desktop/Talk.key". Reply: "Sent. Talk.key is on your laptop's Desktop." | "It sends files between your machines." |
| 4 | 12.0–16.0 | paper | "also start the fine-tune wherever there's a free GPU". `yonder run pc --detach -- 'python train.py'` prints its job line, and the PC chip shows "training". Reply: "Started on the PC. Its RTX 4090 was free." | "It picks the machine with the free GPU." |
| 5 | 16.0–20.0 | paper | The phone slides off, and the terminal widens (you, on laptop). Pills pop: "1Password", "Bitwarden". `yonder secret setup --yes` skips the sleeping raspberry-pi, then one approval gives mac-mini and pc read-only service accounts while the laptop keeps Touch ID. | "Secrets stay in 1Password or Bitwarden." → "One approval sets up your machines." |
| 6 | 20.0–23.0 | paper | `yonder run pc --secret HF_TOKEN -- 'echo $HF_TOKEN'` prints `[HF_TOKEN]`, popping in ember. | "If a command prints a secret, you see [HF_TOKEN]." |
| 7 | 23.0–26.0 | paper | `yonder log`, real output. | "Every command, logged where it ran." |
| 8 | 26.0–30.0 | paper | `brew trust`, `brew install`, then `yonder agents install`. Pills pop: Claude Code, Codex, Pi, Hermes. | "Install with Homebrew. Works in Claude Code, Codex, Pi and Hermes." |
| 9 | 30.0–33.0 | dusk | Back on the horizon with all four machines. Lockup, tagline, repo and platforms. The final chord rings. | "Text one agent. Fix any machine." / github.com/zacharytgray/yonder |

Cuts land on bar or half-bar lines, and pops on beats or half beats. Each caption holds at
least 0.3 s per word once it has settled. Transitions go out, then in, with no crossfade
between two busy layouts. Switching between dusk and paper is a hard cut on the bar.
