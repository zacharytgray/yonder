# soundtrack for the demo, synthesized from scratch. stdlib only.
# usage: python3 music.py out.wav
# 100 bpm, Am F C G. event times match the constants in index.html.
import math
import random
import struct
import sys
import wave

SR = 44100
DUR = 60.0
N = int(SR * DUR)
BEAT = 0.6
BAR = 4 * BEAT
random.seed(7)

music = [0.0] * N   # pad, bass, arp (ducked by the kick)
drums = [0.0] * N
sfx = [0.0] * N


def hz(note):
    names = {'C': -9, 'D': -7, 'E': -5, 'F': -4, 'G': -2, 'A': 0, 'B': 2}
    n = names[note[0]]
    rest = note[1:]
    if rest.startswith('b'):
        n -= 1
        rest = rest[1:]
    return 440.0 * 2 ** ((n + (int(rest) - 4) * 12) / 12)


def add(buf, t0, dur, fn):
    i0 = max(0, int(t0 * SR))
    i1 = min(N, int((t0 + dur) * SR))
    for i in range(i0, i1):
        buf[i] += fn((i - i0) / SR)


def tone(f, amp, decay, harm=((1, 1.0),), attack=0.004):
    tw = [(2 * math.pi * f * h, a) for h, a in harm]

    def fn(x):
        env = min(1.0, x / attack) * math.exp(-x / decay)
        return amp * env * sum(a * math.sin(w * x) for w, a in tw)
    return fn


CHORDS = [
    (['A3', 'C4', 'E4'], 'A2'),
    (['F3', 'A3', 'C4'], 'F2'),
    (['G3', 'C4', 'E4'], 'C3'),
    (['G3', 'B3', 'D4'], 'G2'),
]
BARS = int(DUR / BAR)

# pad: soft, slow attack, overlaps into the next bar
for b in range(BARS):
    notes, _ = CHORDS[b % 4]
    if b == BARS - 1:
        notes, _ = CHORDS[0]
    t0 = b * BAR
    length = BAR + 0.8
    amp = 0.05 if b < 2 else 0.042
    for n in notes:
        f = hz(n)
        for det in (0.997, 1.003):
            w = 2 * math.pi * f * det

            def fn(x, w=w, length=length, amp=amp):
                env = min(1.0, x / 0.5) * min(1.0, max(0.0, (length - x) / 0.8))
                return amp * env * (math.sin(w * x) + 0.25 * math.sin(2 * w * x) + 0.08 * math.sin(3 * w * x))
            add(music, t0, length, fn)

# bass and arp from bar 2, until the last bar
arp = [0.0] * N
for b in range(2, BARS):
    notes, root = CHORDS[b % 4]
    t0 = b * BAR
    last = b == BARS - 1
    if last:
        notes, root = CHORDS[0]
        add(music, t0, BAR, tone(hz(root), 0.16, 1.2, ((1, 1), (2, 0.2))))
        continue
    add(music, t0, 0.55, tone(hz(root), 0.17, 0.35, ((1, 1), (2, 0.25), (3, 0.08))))
    add(music, t0 + 1.5 * BEAT, 0.35, tone(hz(root), 0.12, 0.22, ((1, 1), (2, 0.25))))
    add(music, t0 + 2 * BEAT, 0.55, tone(hz(root), 0.15, 0.35, ((1, 1), (2, 0.25), (3, 0.08))))
    up = [hz(n) * 2 for n in notes]
    pattern = [0, 1, 2, 1, 0, 2, 1, 2]
    for k, p in enumerate(pattern):
        amp = 0.05 if k % 2 == 0 else 0.035
        add(arp, t0 + k * BEAT / 2, 0.4, tone(up[p], amp, 0.16, ((1, 1), (2, 0.15))))

# dotted-eighth echo on the arp
d = int(0.75 * BEAT * SR)
for i in range(d, N):
    arp[i] += 0.32 * arp[i - d]
for i in range(N):
    music[i] += arp[i]

# drums from bar 4 to bar 22 (the outro is drum-free)
kicks = []
for b in range(4, 22):
    t0 = b * BAR
    for beat in (0, 2):
        kicks.append(t0 + beat * BEAT)

        def kick(x):
            ph = 2 * math.pi * (45 * x + (120 - 45) * 0.03 * (1 - math.exp(-x / 0.03)))
            return 0.32 * math.exp(-x / 0.16) * math.sin(ph)
        add(drums, t0 + beat * BEAT, 0.4, kick)
    for e in range(8):
        if e % 2 == 1:
            last = [0.0]

            def hat(x, last=last):
                n = random.uniform(-1, 1)
                v = n - last[0]
                last[0] = n
                return 0.022 * math.exp(-x / 0.03) * v
            add(drums, t0 + e * BEAT / 2, 0.12, hat)
    for beat in (1, 3):
        def rim(x):
            return 0.05 * math.exp(-x / 0.02) * (math.sin(2 * math.pi * 1650 * x) + 0.6 * random.uniform(-1, 1))
        add(drums, t0 + beat * BEAT, 0.1, rim)

# sidechain: duck the music under each kick
duck = [1.0] * N
for k in kicks:
    i0 = int(k * SR)
    for i in range(i0, min(N, i0 + int(0.3 * SR))):
        x = (i - i0) / SR
        duck[i] = min(duck[i], 1 - 0.35 * math.exp(-x / 0.1))
for i in range(N):
    music[i] *= duck[i]

# effects, in key and under the music
def keys(t0, n, cps, amp=0.018):
    for c in range(n):
        last = [0.0]

        def tick(x, last=last):
            v = random.uniform(-1, 1)
            o = v - last[0]
            last[0] = v
            return amp * math.exp(-x / 0.008) * o
        add(sfx, t0 + c / cps + random.uniform(-0.006, 0.006), 0.05, tick)


keys(0.5, 47, 31, 0.012)                 # phone typing
for t0, n in ((10.6, 9), (17.1, 52), (24.3, 74), (39.4, 56), (45.9, 10)):
    keys(t0, n, 26)

def blip(t0, notes, amp=0.07, gap=0.07):
    for k, n in enumerate(notes):
        add(sfx, t0 + k * gap, 0.5, tone(hz(n), amp, 0.14, ((1, 1), (2, 0.1))))


blip(2.1, ['E5', 'A5'])                  # message sent
blip(32.4, ['C5', 'E5'], 0.065)          # reply arrives
for t0 in (11.4, 19.8, 28.2, 42.0, 46.5):  # output lands
    add(sfx, t0, 0.2, tone(hz('A3'), 0.08, 0.05, ((1, 1), (2, 0.3))))
blip(19.8, ['C3', 'A2'], 0.09, 0.12)     # failed
blip(28.2, ['C5', 'E5', 'A5'], 0.055, 0.1)  # running again
add(sfx, 42.0, 1.4, tone(hz('E6'), 0.03, 0.5, ((1, 1), (1.5, 0.4))))  # secret shimmer

# swells into the cuts
for cut in (4.8, 9.6, 31.2, 38.4, 52.8):
    lp = [0.0]

    def swell(x, lp=lp):
        lp[0] += 0.08 * (random.uniform(-1, 1) - lp[0])
        return 0.22 * (x / 0.7) ** 2 * lp[0]
    add(sfx, cut - 0.7, 0.7, swell)

# mix, fade the tail, soft clip, normalize
out = [0.0] * N
for i in range(N):
    out[i] = music[i] + 0.9 * drums[i] + sfx[i]
fade = int(1.5 * SR)
for i in range(N - fade, N):
    out[i] *= (N - i) / fade
for i in range(int(0.05 * SR)):
    out[i] *= i / (0.05 * SR)
out = [math.tanh(1.2 * v) for v in out]
peak = max(abs(v) for v in out) or 1.0
g = 0.89 / peak

with wave.open(sys.argv[1], 'wb') as w:
    w.setnchannels(1)
    w.setsampwidth(2)
    w.setframerate(SR)
    w.writeframes(b''.join(struct.pack('<h', int(v * g * 32767)) for v in out))
