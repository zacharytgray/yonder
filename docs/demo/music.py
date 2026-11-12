# soundtrack for the demo, synthesized from scratch. stdlib only.
# usage: python3 music.py out.wav
# 120 bpm in G, G C D G a bar each: whistled hook, held upright bass roots,
# soft electric piano on 2 and 4, kick, claps and shaker.
# event times match the constants in index.html. build.sh sets the loudness.
import math
import random
import struct
import sys
import wave

SR = 44100
DUR = 33.0
N = int(SR * DUR)
BEAT = 0.5
BAR = 4 * BEAT
random.seed(11)
out = [0.0] * N
keys = [0.0] * N   # electric piano, low-passed before the mix
low = [0.0] * N    # bass, low-passed before the mix


def hz(note):
    names = {'C': -9, 'D': -7, 'E': -5, 'F': -4, 'G': -2, 'A': 0, 'B': 2}
    n = names[note[0]]
    rest = note[1:]
    if rest.startswith('#'):
        n += 1
        rest = rest[1:]
    return 440.0 * 2 ** ((n + (int(rest) - 4) * 12) / 12)


def place(t0, samples, amp=1.0, buf=None):
    buf = out if buf is None else buf
    i0 = int(t0 * SR)
    for k, v in enumerate(samples):
        i = i0 + k
        if 0 <= i < N:
            buf[i] += amp * v


def lowpass(buf, fc):
    # two one-pole passes
    k = 1 - math.exp(-2 * math.pi * fc / SR)
    for _ in range(2):
        y = 0.0
        for i in range(N):
            y += k * (buf[i] - y)
            buf[i] = y


def pluck(f, dur, decay=0.996):
    # karplus-strong with an allpass for the fractional part of the delay, so it stays in tune
    d = SR / f - 0.5
    p = int(d)
    frac = d - p
    if frac < 0.1:
        p -= 1
        frac += 1
    c = (1 - frac) / (1 + frac)
    buf = [random.uniform(-1, 1) for _ in range(p)]
    n = int(dur * SR)
    res = [0.0] * n
    fade = int(0.03 * SR)
    prev = ax = ay = 0.0
    j = 0
    for i in range(n):
        s = buf[j]
        a = 0.5 * (s + prev)
        prev = s
        ay = c * a + ax - c * ay
        ax = a
        buf[j] = decay * ay
        j = j + 1 if j + 1 < p else 0
        res[i] = s if i < n - fade else s * (n - i) / fade
    return res


def noise(dur, decay, hp=True, lp=1.0):
    n = int(dur * SR)
    res, last, y = [0.0] * n, 0.0, 0.0
    for i in range(n):
        v = random.uniform(-1, 1)
        x = v - last if hp else v
        last = v
        y += lp * (x - y)
        res[i] = y * math.exp(-i / SR / decay)
    return res


def kick(amp):
    n = int(0.3 * SR)
    return [amp * math.exp(-i / SR / 0.12) * math.sin(2 * math.pi * (50 * i / SR + 2.4 * (1 - math.exp(-i / SR / 0.03)))) for i in range(n)]


def clap(amp):
    res = [0.0] * int(0.2 * SR)
    for k, d in enumerate((0.0, 0.009, 0.018)):
        burst = noise(0.012, 0.004, lp=0.5)
        i0 = int(d * SR)
        for i, v in enumerate(burst):
            res[i0 + i] += v * (0.7 + 0.15 * k)
    tail = noise(0.17, 0.05, lp=0.45)
    i0 = int(0.022 * SR)
    for i, v in enumerate(tail):
        if i0 + i < len(res):
            res[i0 + i] += 0.8 * v
    return [amp * v for v in res]


def epiano(t0, f, dur, amp):
    # soft tine: sine with a little second harmonic that fades fast, no pick noise
    i0, n = int(t0 * SR), int(dur * SR)
    rel = int(0.08 * SR)
    for i in range(n):
        x = i / SR
        env = min(1, x / 0.008) * math.exp(-x / 0.45) * (1 if i < n - rel else (n - i) / rel)
        v = math.sin(2 * math.pi * f * x) + 0.12 * math.exp(-x / 0.12) * math.sin(4 * math.pi * f * x)
        if 0 <= i0 + i < N:
            keys[i0 + i] += amp * env * v


def bass(t0, note, dur, decay):
    # soft upright: sine plus a little second harmonic, slow attack
    f, n = hz(note), int(dur * SR)
    rel = int(0.12 * SR)
    res = [0.0] * n
    for i in range(n):
        x = i / SR
        env = min(1, x / 0.04) * math.exp(-x / decay) * (1 if i < n - rel else (n - i) / rel)
        res[i] = env * (math.sin(2 * math.pi * f * x) + 0.18 * math.sin(4 * math.pi * f * x))
    place(t0, res, 0.135, low)


# one chord a bar: the piano voicing, and the bass root (steps and fourths, no octave jumps)
CHORDS = {
    'G': (['G3', 'B3', 'D4'], 'G2'),
    'C': (['G3', 'C4', 'E4'], 'C2'),
    'D': (['F#3', 'A3', 'D4'], 'D2'),
}
PROG = ['G', 'C', 'D', 'G'] * 4 + ['G']
END = 32.0  # the final chord; it rings out to 33s

for b, name in enumerate(PROG):
    t0 = b * BAR
    chord, root = CHORDS[name]
    if t0 >= END:
        bass(t0, root, 1.0, 1.2)
        for k, n in enumerate(chord + ['G4']):
            epiano(t0 + k * 0.015, hz(n), 1.0, 0.075)
        place(t0, kick(0.45))
        place(t0, clap(0.22))
        continue
    # the root held for the bar, and a piano chord on 2 and 4
    bass(t0, root, BAR + 0.04, 1.8)
    for beat in (1, 3):
        for k, n in enumerate(chord):
            epiano(t0 + beat * BEAT + k * 0.006, hz(n), 0.42, 0.07)
    # drums: kick on 1 and 3 (four on the floor in the build), claps on 2 and 4
    build = 13 <= b <= 14
    for beat in range(4):
        if beat in (0, 2) or build:
            place(t0 + beat * BEAT, kick(0.38))
        if beat in (1, 3):
            place(t0 + beat * BEAT, clap(0.2))
    # shaker: offbeat eighths, sixteenths in the build
    steps = 16 if build else 8
    for s in range(steps):
        if steps == 8 and s % 2 == 0:
            continue
        place(t0 + s * BAR / steps, noise(0.05, 0.015), 0.03 if steps == 8 else 0.022)


def whistle(notes, amp=0.12):
    prev = None
    for beat, beats, name in notes:
        f = hz(name)
        t0, dur = beat * BEAT, beats * BEAT - 0.02
        n = int(dur * SR)
        ph, res = 0.0, [0.0] * n
        for i in range(n):
            x = i / SR
            g = f if prev is None or x > 0.04 else prev + (f - prev) * (x / 0.04)
            vib = 1 + (0.007 * math.sin(2 * math.pi * 5.5 * x) if x > 0.12 else 0)
            ph += 2 * math.pi * g * vib / SR
            env = min(1, x / 0.03) * min(1, max(0, (dur - x) / 0.05))
            res[i] = env * (math.sin(ph) + 0.04 * math.sin(2 * ph) + 0.02 * random.uniform(-1, 1))
        place(t0, res, amp)
        prev = f


lowpass(keys, 1600)
lowpass(low, 350)
for i in range(N):
    out[i] += keys[i]
for i in range(N):
    out[i] += low[i]

# whistled hook over the title, the answer in the middle, the hook again at the end
whistle([(0, 0.5, 'D5'), (0.5, 0.5, 'G5'), (1, 0.5, 'A5'), (1.5, 1, 'B5'), (2.5, 0.5, 'A5'), (3, 1, 'G5'),
         (4, 0.5, 'E5'), (4.5, 0.5, 'G5'), (5, 1, 'A5'), (6, 1.5, 'G5')])
whistle([(28, 0.5, 'B5'), (28.5, 0.5, 'A5'), (29, 0.5, 'G5'), (29.5, 0.5, 'A5'), (30, 1.5, 'B5')], 0.08)
whistle([(60, 0.5, 'D5'), (60.5, 0.5, 'G5'), (61, 0.5, 'A5'), (61.5, 1, 'B5'), (62.5, 0.5, 'D6'), (63, 1, 'B5'), (64, 2, 'G5')])

# effects: a quiet tuned pluck on each pop, key ticks, swishes into cuts
for t, n in ((0.25, 'D6'), (1.0, 'G6'), (1.5, 'B5'), (2.0, 'D6'), (2.5, 'G6'), (4.25, 'D6'), (6.0, 'B5'),
             (10.0, 'D6'), (10.75, 'G6'), (12.0, 'D6'), (13.75, 'B5'), (14.5, 'G6'), (16.25, 'D6'), (16.5, 'G6'),
             (28.25, 'B5'), (28.5, 'D6'), (28.75, 'G6'), (30.1, 'D6'), (30.45, 'G6')):
    place(t, pluck(hz(n), 0.3, 0.99), 0.07)
for t, n in ((21.5, 'G5'), (21.56, 'B5'), (21.62, 'D6'), (21.68, 'G6')):  # [HF_TOKEN]
    place(t, pluck(hz(n), 0.4, 0.992), 0.08)
for t0, n, cps in ((4.5, 9, 40), (8.25, 20, 50), (9.0, 49, 60), (12.75, 43, 60), (16.5, 25, 60), (20.25, 51, 60),
                   (23.25, 10, 40), (26.25, 45, 70), (27.0, 47, 70), (27.75, 21, 60)):
    for c in range(0, n, 2):
        place(t0 + c / cps, noise(0.02, 0.004), 0.025)
for cut in (4.0, 8.0, 12.0, 16.0, 20.0, 23.0, 26.0, 30.0):
    sw = noise(0.16, 1.0, hp=False, lp=0.12)
    m = len(sw)
    place(cut - 0.16, [v * (i / m) ** 2 for i, v in enumerate(sw)], 0.35)

# tail fade, soft clip, peak normalize; build.sh brings it to -16 LUFS
fade = int(0.3 * SR)
for i in range(N - fade, N):
    out[i] *= (N - i) / fade
out = [math.tanh(1.1 * v) for v in out]
peak = max(abs(v) for v in out) or 1.0
g = 0.9 / peak
with wave.open(sys.argv[1], 'wb') as w:
    w.setnchannels(1)
    w.setsampwidth(2)
    w.setframerate(SR)
    w.writeframes(b''.join(struct.pack('<h', int(v * g * 32767)) for v in out))
