# Yonder brand

How Yonder looks and sounds. This page is for anyone building a Yonder surface: the README,
the website, a portfolio page, a slide, a sticker. The assets are in [`docs/brand/`](brand/).
The tokens are in [`tokens.css`](brand/tokens.css) and [`tokens.json`](brand/tokens.json).

<p>
  <img src="brand/yonder-logo-light.svg" height="56" alt="Yonder logo on light">
  &nbsp;&nbsp;
  <img src="brand/yonder-icon.svg" height="56" alt="Yonder icon">
</p>

## The idea

"Yonder" means over there, at a distance, the place you can see but can't reach from where you
stand. Yonder the tool is about reaching it anyway: you talk to one agent here, and it fixes a
machine out there.

So the brand looks toward the horizon. It is calm and open, like the last hour of light on a
long, flat stretch of country. The name also has a little twang to it, an old word your
grandfather might use. The brand keeps that: a soft, slightly wonky serif, a wry line now and
then. It never puts on a costume. No cowboy hats, no rope type, no wanted posters.

**The image to hold on to:** a sun setting on a long horizon line, its light trailing toward
you across the ground, and one small light already up in the sky.

## Voice

Yonder talks like a calm person who knows the work and doesn't need to sell it.

- **Plain words, short sentences.** One idea per sentence. Say what the command does.
- **Calm.** No exclamation marks, no "supercharge", no "seamless", no urgency.
- **Honest about limits.** Say what Yonder does not do, in the same plain voice as what it does.
  The security wording in the README is the model: exact, unhedged, no marketing.
- **A little wry, a little old-timey, sparingly.** One turn of phrase per page at most, and
  only where it is also true. "Fails fast, says when it was last seen" is fine. "Howdy,
  partner" is not.
- **Second person for the reader, first person for the author.** "You need Tailscale on each
  machine." Agents are "your agent", machines are "your machines".
- **Example names only.** `gpu-box`, `laptop`, `mini`, `100.64.0.x`, `example.ts.net`.

| Say | Don't say |
|---|---|
| Text one agent. Fix any machine. | Unleash the power of your fleet! |
| `gpu-box` is offline (last seen 2h ago). | Oops! Something went wrong. |
| Yonder connects machines. It does not decide what your agent may do. | Enterprise-grade security for your AI workflows. |
| Out yonder, on the GPU box. (once, in a headline) | Yeehaw! Saddle up your servers. |

**Tagline:** Text one agent. Fix any machine.
**One line:** Yonder connects your own computers so the AI agents on any of them can use all of
them.

## Color

Four families, named for the scene: **dusk** (the sky going dark, and the ink), **horizon**
(the setting sun), **prairie** (the ground), **sky** (the cool light still left). **Lantern**
is a small extra for warnings. Paper is the warm light ground.

### Palette

| Token | Hex | Use |
|---|---|---|
| `dusk-900` | `#1A1828` | Night. Dark ground. |
| `dusk-800` | `#26223D` | Dusk ink. Text on light, the horizon line in the mark. |
| `dusk-700` | `#36314A` | Rules on dark. |
| `dusk-600` | `#5E5870` | Muted text on light. |
| `dusk-300` | `#ABA3B8` | Muted text on dark. |
| `horizon-600` | `#A3431A` | Links and buttons on light. |
| `horizon-500` | `#D2602F` | The sun. Graphics and large type on light. |
| `horizon-300` | `#F08A55` | The sun on dark. Links and buttons on dark. |
| `prairie-600` | `#4F6B42` | "Online" text on light. |
| `prairie-400` | `#6F8F5E` | Near ridges in illustration. |
| `prairie-200` | `#9FBF8C` | "Online" text on dark. |
| `sky-600` | `#3D6680` | Info text and focus ring on light. |
| `sky-400` | `#7FA6BF` | Far ridges, sky in illustration. |
| `sky-200` | `#A9C8DA` | Info text and focus ring on dark. |
| `lantern-700` | `#8A5A12` | Warning text on light. |
| `lantern-500` | `#E3A23A` | The lantern: a small lit point in illustration. |
| `lantern-300` | `#F2B54E` | Warning text on dark. |
| `paper-50` | `#FBF7EF` | Raised surface on light. |
| `paper-100` | `#F6F0E4` | Paper. Light ground. |
| `paper-200` | `#EFE6D5` | Bands and code blocks on light. |
| `paper-300` | `#DDD0BA` | Rules on light. |
| `cream` | `#F3EADA` | Text on dark, the horizon line in the dark mark. |

### Theme tokens

Use these in code, not the palette names. `tokens.css` switches them on
`prefers-color-scheme`, and `data-theme="light"` or `"dark"` on `<html>` overrides it.

| Token | Light | Dark |
|---|---|---|
| `--y-bg` | `#F6F0E4` | `#1A1828` |
| `--y-bg-raised` | `#FBF7EF` | `#221F33` |
| `--y-band` | `#EFE6D5` | `#201D30` |
| `--y-line` | `#DDD0BA` | `#36314A` |
| `--y-text` | `#26223D` | `#F3EADA` |
| `--y-text-muted` | `#5E5870` | `#ABA3B8` |
| `--y-accent` | `#D2602F` | `#F08A55` |
| `--y-link` | `#A3431A` | `#F08A55` |
| `--y-button-bg` / `--y-button-text` | `#A3431A` / `#FFFFFF` | `#F08A55` / `#1A1828` |
| `--y-focus` | `#3D6680` | `#A9C8DA` |
| `--y-online` | `#4F6B42` | `#9FBF8C` |
| `--y-info` | `#3D6680` | `#A9C8DA` |
| `--y-warn` | `#8A5A12` | `#F2B54E` |
| `--y-offline` | `#5E5870` | `#ABA3B8` |
| `--y-code-bg` | `#EFE6D5` | `#221F33` |

### Contrast

Checked with the WCAG 2 formula. Every text pair passes AA (4.5:1).

| Pair | Light | Dark |
|---|---|---|
| text on bg | 13.4 | 14.6 |
| muted on bg | 6.0 | 7.2 |
| link on bg | 5.5 | 7.0 |
| link on band | 5.0 | 6.6 |
| button text on button | 6.2 | 7.0 |
| online on bg | 5.3 | 8.6 |
| info on bg | 5.4 | 9.9 |
| warn on bg | 5.2 | 9.5 |
| code text on code bg | 12.3 | 13.4 |

`horizon-500` on paper is 3.4:1. That passes for graphics and for type 24 px and up, so keep
it to the mark, illustration and big display type. Small orange text on light uses
`horizon-600`.

### Proportions

Mostly ground. Paper or night covers most of any surface, ink carries the text, and the sun
color appears in a few places: the mark, one button, links. Prairie, sky and lantern are for
state (online, info, warning) and illustration. A page that looks orange has too much orange.

## Type

All three families are free under the SIL Open Font License 1.1. They can be self-hosted, used
on the web, and embedded in images and PDFs.

| Role | Family | Settings | Source |
|---|---|---|---|
| Display: logo, headlines, the tagline | **Fraunces** | weight 500–600, `SOFT` 100, `WONK` 1, `opsz` 144 (72 for subheads) | [Google Fonts](https://fonts.google.com/specimen/Fraunces), [repo](https://github.com/undercasetype/Fraunces) |
| Body: prose, UI, buttons | **Instrument Sans** | 400 body, 500 labels, 600 strong | [Google Fonts](https://fonts.google.com/specimen/Instrument+Sans), [repo](https://github.com/Instrument/instrument-sans) |
| Mono: commands, output, machine names | **JetBrains Mono** | 400, 500 | [Google Fonts](https://fonts.google.com/specimen/JetBrains+Mono), [repo](https://github.com/JetBrains/JetBrainsMono) |

Fraunces at full softness and with its wonky alternates is where the twang lives: round
terminals, a slightly bouncing rhythm, a "y" with a ball tail. Use it large and sparingly.
Headlines are sentence case. Never set body copy in it.

Instrument Sans is the working voice. JetBrains Mono shows anything a person or agent would
type, and the names of machines.

- Body size 16–18 px, line height 1.6, measure about 40rem (65 characters).
- Headlines line height 1.1, tracking 0 to -0.01em. Never track Fraunces wide.
- No all-caps eyebrows over every heading. If you need a label, use Instrument Sans 500 in
  sentence case and `--y-text-muted`.
- Fallbacks when the web fonts aren't loaded: Iowan Old Style or Georgia for display,
  system-ui for body, ui-monospace for mono. They are in the token stacks.

## Logo

<p>
  <img src="brand/yonder-logo-light.svg" height="48" alt="Yonder logo, light">
</p>

**The mark** is a sun setting on a horizon line. Two bars of reflected light trail toward you
below the line, getting shorter as they come closer. One small dot sits high in the sky to the
left: the first light up, the other machine out yonder that's already on. The horizon line is
the longest thing in it; it is the point of the whole drawing.

**The wordmark** is "yonder" in lowercase Fraunces SemiBold, soft and wonky, converted to
outlines. The name is always lowercase in the logo. In running text it is "Yonder", and the
command is `yonder`.

**The lockup** puts the mark left of the wordmark, with the horizon line sitting on the
wordmark's baseline. The sun rises above the baseline like the x-height letters; the reflection
drops into the descender, level with the tail of the "y".

### Files

| Need | File |
|---|---|
| Logo on light (README, docs, site header) | `brand/yonder-logo-light.svg` |
| Logo on dark | `brand/yonder-logo-dark.svg` |
| Mark alone, when the name is nearby | `brand/yonder-mark-light.svg`, `-dark.svg` |
| Wordmark alone | `brand/yonder-wordmark-light.svg`, `-dark.svg` |
| App icon, favicon, avatar | `brand/yonder-icon.svg` (vector), `yonder-icon-32.png`, `-180.png` (apple-touch), `-512.png` |
| Link previews (GitHub social preview, Open Graph) | `brand/social-preview.png` (1280×640), source `social-preview.svg` |

"Light" and "dark" name the background the file is made for. The SVGs have no background; the
icon and the social preview are opaque.

### Rules

- **Clear space:** the sun's radius (a quarter of the mark's width) on every side.
- **Minimum size:** lockup 120 px wide, mark 24 px. Below 24 px use the icon, which drops the
  dot and one reflection bar so it still reads at 16 px.
- **Color:** ink plus sun color only. Dark ink and `horizon-500` on light grounds;
  cream and `horizon-300` on dark. On a photo or a busy ground, put the logo on a paper or
  night panel.
- **One color:** if you only have one color, use the ink color for everything and drop the
  reflection bars' transparency. Don't make a one-color orange logo.
- Don't rotate, stretch, outline, add shadows or glows, or put the sun above the line.
- Don't redraw the wordmark in a live font. Use the files; they are outlined so they look the
  same everywhere.
- Don't swap the lowercase wordmark for capitals.
- Don't add a hat, a rope, a cactus, a lasso, a tumbleweed or a sheriff's star. Ever.

## Iconography

When the site or docs need icons, draw them the way the mark is drawn.

- 24 px grid, 2 px strokes, round caps and joins, mostly horizontal strokes.
- Ink color, with at most one element in the sun color.
- Simple, level shapes: a line, a dot, a half-disc, a ridge. No perspective, no 3D.
- Machine states use the mark's language:
  - **online:** a filled dot, `--y-online`
  - **offline:** a hollow ring, `--y-offline`, with "last seen" text beside it. Never red.
  - **this machine:** a dot with a short horizon bar under it
- Use an existing open set (Lucide, ISC license, at 2 px stroke) for generic UI such as copy,
  external link or menu, rather than drawing your own.

## Illustration and motifs

The brand has one landscape, seen at different distances.

- **The horizon line.** A single long horizontal rule, ink or cream, edge to edge. It is the
  brand's main divider: under a hero, between sections, across the social preview. Thicker
  than a normal rule (3–6 px at page scale).
- **The sun.** A half-disc sitting on the line, flat color, no gradient inside it. It goes
  off-center, to the right: yonder is over there.
- **Reflections.** Two or three rounded bars under the sun, each shorter and fainter as they
  come toward the viewer (opacity about 0.55, 0.35, 0.2).
- **Distant ridges.** Low, soft silhouettes on the line, one or two layers, close to the
  ground color. They sit in front of the sun's base. They are hills, not peaks: no snow caps,
  no jagged mountains.
- **The lantern.** One small lit point, `lantern-500` or the ink color, far off on a ridge or
  in the sky. It stands for a machine out there that's online. One per scene.
- **The sky.** On dark, a slow vertical gradient from `dusk-900` at the top to about
  `#2E2440` at the horizon is allowed. It is the only gradient in the brand. Light surfaces
  stay flat paper.

Composition: lots of empty sky, the horizon in the lower third, text in the open space
above it. Command lines and terminal output can sit "on the ground" under the line, in mono.

Motion, if any: the sun sinks a few pixels or a lantern fades on, once, over about 600 ms
with `--y-ease`. Nothing loops, nothing bounces, and all of it turns off under
`prefers-reduced-motion`.

## Do and don't

| Do | Don't |
|---|---|
| Leave room. Big sky, low horizon. | Fill every corner with cards and stats. |
| Show the real thing: a terminal, `yonder ls`, the actual output. | Fake dashboards, fake metrics, user counts. |
| Use the sun color in a few places. | Orange backgrounds, orange body text, orange everywhere. |
| Flat fills, one horizon gradient on dark at most. | Glow, bloom, neon, glassy blur, gradient buttons. |
| Fraunces big and rare, Instrument Sans for everything else. | Fraunces paragraphs, or a third display face. |
| Calm words, one wry line if it's true. | Western puns, "y'all", "howdy", "partner". |
| Example machine names in every screenshot and sample. | Real hostnames, IPs, tailnet names or emails. |
| Say what it doesn't do. | Hide the limits below the fold. |

## Badges and small UI

Shields.io badges use the dusk ink as the label and the sun as the value, flat-square:
`?style=flat-square&labelColor=26223d&color=d2602f`. Keyboard-style links use GitHub's
`<kbd>` element, as in the README nav row.
