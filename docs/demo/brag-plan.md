# Yonder demo: plan

Made with the brag skill (github.com/latent-spaces/brag, the `/brag-slim` workflow). brag
aims for 15–25 seconds; this one runs about a minute on purpose, to match the README's
"one-minute demo" bar.

## Rubric

- **What it is:** a CLI that connects your own machines so the agent on one can use all of them.
- **Who it's for:** people with a laptop, an always-on box and a GPU box, who talk to an agent.
- **What sets it apart:** one text to one agent fixes a different machine. No dashboard.
- **Best claim:** "Text one agent. Fix any machine."
- **Visual hook:** a phone message, "ollama on the gpu box is down, can you fix it?"
- **Real UI shown:** `yonder ls` and `yonder log`, captured from the binary against a fake
  fleet (`YONDER_HOME` + a crafted tailscale status). The `yonder run` output is staged,
  since there is no real gpu-box to ssh into. The secret line is what the scrubber prints
  when a command echoes the value.
- **Tone:** `polished`, with terminal realism. Calm, specific, no hype.
- **Share caption:** see `share-copy.txt`.

Every name is an example: `laptop`, `mini`, `gpu-box`, `example.ts.net`, user `me`.

## Identity

The Yonder brand, dark variant (`docs/brand.md`). Night `#1A1828` fading to dusk `#2E2440` at
the horizon, raised panels `#221F33`, rules `#36314A`, cream text `#F3EADA`, muted `#ABA3B8`.
Sun `#F08A55` is the one accent: caption highlights, the gpu-box row, `[HF_TOKEN]`, the
"agent here" tag. Prairie `#9FBF8C` for online, lantern `#F2B54E` for the failed service,
sky `#A9C8DA` for the link between machines and the bubble you send.
Fraunces (soft, wonky) for the tagline and captions, Instrument Sans for UI text, JetBrains
Mono for commands and machine names, all bundled in `fonts/`. The title and outro use the
brand lockup, and the phone's avatar is the brand icon.

## Music

100 BPM, 4/4, so a beat is 0.6s and a bar 2.4s. Am – F – C – G, one chord a bar.
Pad alone under the hook, arp and bass from the title, soft kick and hats from the first
command, drums out for the outro. Every cut is on a bar line. Effects (send blip, reply
blip, key ticks, cut swells) are in key and sit under the music.

## Storyboard (25 bars, 60.0s)

| # | Bars | Time | Scene | Text on screen |
|---|---|---|---|---|
| 1 | 0–1 | 0.0–4.8 | Hook. A phone, center. The message types and sends. | "ollama on the gpu box is down, can you fix it?" |
| 2 | 2–3 | 4.8–9.6 | Title. Lockup, then the line. | brand lockup / "Text one agent. Fix any machine." |
| 3 | 4–6 | 9.6–16.8 | Phone slides left, agent typing. Terminal on `mini` runs `yonder ls`; rows land one by one, the fleet chips light up, the gpu-box row highlights. | "It sees every machine." |
| 4 | 7–9 | 16.8–24.0 | `yonder run gpu-box -- systemctl --user status ollama`. A link draws mini → gpu-box. Output: failed, oom-kill. The gpu-box chip turns red. | "It runs commands there, over your own tailnet." |
| 5 | 10–12 | 24.0–31.2 | `yonder run gpu-box -- 'systemctl --user restart ollama && ollama list'`. Three models list. The chip turns green. | "It fixes it, and checks." |
| 6 | 13–15 | 31.2–38.4 | Phone back to center. The agent's reply arrives. | "Ollama on gpu-box ran out of memory and stopped. I restarted it. All three models are back." |
| 7 | 16–18 | 38.4–45.6 | Full-width terminal, big type. `yonder run gpu-box --secret HF_TOKEN -- 'echo $HF_TOKEN'` prints `[HF_TOKEN]`. | "Secrets go by name. If a command prints one, you see the name." |
| 8 | 19–21 | 45.6–52.8 | `yonder log`, real output, rows land one by one. | "Every command, logged where it ran." |
| 9 | 22–24 | 52.8–60.0 | Outro. Lockup, line, repo, platforms. Drums drop out, last chord rings. | "Text one agent. Fix any machine." / github.com/zacharytgray/yonder |

Output lands, the send and reply blips and the chip color changes all fall on beats or half
beats.

Transitions: old content out, then new content in, with a dip through the background. No
crossfades between two busy layouts.
