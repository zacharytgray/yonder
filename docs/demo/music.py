# soundtrack for the demo, synthesized from scratch. stdlib only.
# usage: python3 music.py out.wav
# 92 bpm in D major, plain triads, one chord a bar: D, A, Bm, G, twice, then G and A a half
# bar each, and D held under the outro. a soft felt piano (chords and a simple top line),
# a light pad, a felt kick on 1 and 3 and a quiet brush on 2 and 4 once the scenes start,
# and a small room. every note is in D major.
# the outro starts on the final chord, at 9 bars: index.html uses the same BAR.
# build.sh sets the loudness.
import math
import random
import struct
import sys
import wave

SR = 44100
BPM = 92
BEAT = 60 / BPM
BAR = 4 * BEAT
END = 9  # the final chord, under the outro
DUR = END * BAR + 4.2
N = int(SR * DUR)
random.seed(7)

keys = [0.0] * N
pad = [0.0] * N
drums = [0.0] * N

NAMES = {'C': 0, 'D': 2, 'E': 4, 'F': 5, 'G': 7, 'A': 9, 'B': 11}


def midi(note):
    n = NAMES[note[0]]
    rest = note[1:]
    if rest.startswith('#'):
        n += 1
        rest = rest[1:]
    return n + 12 * (int(rest) + 1)


def hz(m):
    return 440.0 * 2 ** ((m - 69) / 12)


def lowpass(buf, fc, passes=2):
    k = 1 - math.exp(-2 * math.pi * fc / SR)
    for _ in range(passes):
        y = 0.0
        for i in range(len(buf)):
            y += k * (buf[i] - y)
            buf[i] = y


def highpass(buf, fc):
    k = 1 - math.exp(-2 * math.pi * fc / SR)
    y = 0.0
    for i in range(len(buf)):
        y += k * (buf[i] - y)
        buf[i] -= y


_notes = {}


def piano_note(m, n):
    # felt piano: a few slightly stretched partials that die faster as they go up,
    # two strings a hair apart on the lowest two, a slow attack and a damper at the end
    key = (m, n)
    if key in _notes:
        return _notes[key]
    f = hz(m)
    out = [0.0] * n
    att, rel = int(0.008 * SR), min(int(0.3 * SR), n // 2)
    base = 0.55 * (f / 262) ** 0.35  # low notes ring longer
    for p in range(1, 8):
        fp = p * f * math.sqrt(1 + 0.0003 * p * p)
        if fp > 6000:
            break
        a = 0.9 ** (p - 1) / p ** 1.2
        for detune in ((1.0, 1.0007) if p <= 2 else (1.0,)):
            w = 2 * math.pi * fp * detune / SR
            c = 2 * math.cos(w)
            s1, s2 = math.sin(-w), math.sin(-2 * w)
            fast = math.exp(-base * (1 + 0.7 * (p - 1)) * 2.2 / SR)
            slow = math.exp(-base * (1 + 0.7 * (p - 1)) * 0.45 / SR)
            ef, es = 0.6, 0.4
            amp = a / (2 if p <= 2 else 1)
            for i in range(n):
                s = c * s1 - s2
                s2, s1 = s1, s
                out[i] += amp * (ef + es) * s
                ef *= fast
                es *= slow
    for i in range(n):
        g = 1.0
        if i < att:
            g = i / att
        if i > n - rel:
            g *= (n - i) / rel
        out[i] *= g
    _notes[key] = out
    return out


def piano(t0, note, dur, amp):
    i0, n = int(t0 * SR), int(dur * SR)
    n = min(n, N - i0)
    if n <= 0:
        return
    buf = piano_note(midi(note), n)
    for i in range(n):
        keys[i0 + i] += amp * buf[i]


def pad_chord(t0, notes, dur, amp):
    # two detuned sines and a soft octave per note, slow swell in and out
    i0, n = int(t0 * SR), int(dur * SR)
    att, rel = int(0.8 * SR), int(0.9 * SR)
    for note in notes:
        f = hz(midi(note))
        ws = [2 * math.pi * f * d / SR for d in (0.998, 1.002)]
        w2 = 4 * math.pi * f / SR
        ph = [random.uniform(0, 6.28) for _ in range(3)]
        for i in range(n):
            j = i0 + i
            if j >= N:
                break
            env = min(1, i / att) * min(1, (n - i) / rel)
            v = 0.5 * (math.sin(ws[0] * i + ph[0]) + math.sin(ws[1] * i + ph[1])) + 0.15 * math.sin(w2 * i + ph[2])
            pad[j] += amp * env * v


def kick(t0, amp):
    # a soft low thump, like a felt beater
    i0, n = int(t0 * SR), int(0.35 * SR)
    ph = 0.0
    for i in range(n):
        j = i0 + i
        if j >= N:
            break
        x = i / SR
        f = 55 + 35 * math.exp(-x / 0.03)
        ph += 2 * math.pi * f / SR
        drums[j] += amp * min(1, x / 0.004) * math.exp(-x / 0.12) * math.sin(ph)


def brush(t0, amp):
    # a quiet brushed tap: band-limited noise with a short tail
    i0, n = int(t0 * SR), int(0.18 * SR)
    lo = hi = 0.0
    for i in range(n):
        j = i0 + i
        if j >= N:
            break
        x = i / SR
        r = random.uniform(-1, 1)
        lo += 0.35 * (r - lo)
        hi += 0.02 * (lo - hi)
        drums[j] += amp * min(1, x / 0.006) * math.exp(-x / 0.05) * (lo - hi)


# chords: right hand, left-hand root and fifth, pad
D = (['A3', 'D4', 'F#4'], 'D2', 'A2', ['D3', 'A3', 'F#4'])
A = (['A3', 'C#4', 'E4'], 'A2', 'E3', ['A2', 'E3', 'C#4'])
BM = (['B3', 'D4', 'F#4'], 'B2', 'F#3', ['B2', 'F#3', 'D4'])
G = (['B3', 'D4', 'G4'], 'G2', 'D3', ['G2', 'D3', 'B3'])
PROG = [(0, 1, D), (1, 1, A), (2, 1, BM), (3, 1, G), (4, 1, D), (5, 1, A), (6, 1, BM), (7, 1, G),
        (8, 0.5, G), (8.5, 0.5, A)]
# the top line, (bar, beat, note, beats): chord tones, mostly steps
MELODY = [
    (1, 0, 'C#5', 2), (1, 2, 'E5', 2),
    (2, 0, 'D5', 2), (2, 2, 'F#5', 1), (2, 3, 'E5', 1),
    (3, 0, 'D5', 3), (3, 3, 'B4', 1),
    (4, 0, 'A4', 1), (4, 1, 'D5', 1), (4, 2, 'F#5', 2),
    (5, 0, 'E5', 2), (5, 2, 'C#5', 1), (5, 3, 'E5', 1),
    (6, 0, 'F#5', 1), (6, 1, 'E5', 1), (6, 2, 'D5', 2),
    (7, 0, 'B4', 2), (7, 2, 'D5', 2),
    (8, 0, 'D5', 2), (8, 2, 'C#5', 2),
]
PULSE_FROM = 1.25 * BAR  # the first scene starts on beat 2 of bar 1

for start, length, (rh, root, fifth, pad_notes) in PROG:
    t0 = start * BAR
    pad_chord(t0, pad_notes, length * BAR + 0.8, 0.035)
    piano(t0, root, length * BAR + 0.3, 0.11)
    for k, nt in enumerate(rh):
        piano(t0 + 0.012 * k, nt, 2 * BEAT + 0.15, 0.085)
    if length == 1:
        piano(t0 + 2 * BEAT, fifth, 2 * BEAT + 0.3, 0.07)
        for k, nt in enumerate(rh):
            piano(t0 + 2 * BEAT + 0.01 * k, nt, 2 * BEAT + 0.15, 0.06)
    for b in range(int(length * 4)):
        t = t0 + b * BEAT
        if t >= PULSE_FROM - 0.01:
            if b % 2 == 0:
                kick(t, 0.09 if b == 0 else 0.065)
            else:
                brush(t, 0.05)

for bar, beat, note, beats in MELODY:
    piano(bar * BAR + beat * BEAT, note, beats * BEAT + 0.2, 0.13)

t0 = END * BAR
pad_chord(t0, ['D3', 'A3', 'F#4'], DUR - t0, 0.035)
piano(t0, 'D2', DUR - t0, 0.11)
piano(t0, 'D3', DUR - t0, 0.08)
for k, nt in enumerate(['A3', 'D4', 'F#4']):
    piano(t0 + 0.02 * k, nt, DUR - t0, 0.085)
piano(t0 + 0.08, 'D5', DUR - t0, 0.13)
kick(t0, 0.09)

lowpass(keys, 3800)
highpass(keys, 45)
lowpass(pad, 1200)
highpass(pad, 80)
lowpass(drums, 2400, 1)


def comb(src, delay, fb, damp):
    out, buf, j, y = [0.0] * N, [0.0] * delay, 0, 0.0
    for i in range(N):
        o = buf[j]
        y = o * (1 - damp) + y * damp
        buf[j] = src[i] + y * fb
        j = j + 1 if j + 1 < delay else 0
        out[i] = o
    return out


def allpass(src, delay, g=0.5):
    out, buf, j = [0.0] * N, [0.0] * delay, 0
    for i in range(N):
        b = buf[j]
        x = src[i] + b * g
        buf[j] = x
        out[i] = b - x * g
        j = j + 1 if j + 1 < delay else 0
    return out


# a small warm room on the piano and pad, different combs left and right
send = [keys[i] + 0.5 * pad[i] for i in range(N)]
lowpass(send, 2800, 1)
rooms = []
for spread in (0, 23):
    acc = [0.0] * N
    for d in (1116, 1188, 1277, 1356):
        c = comb(send, d + spread, 0.8, 0.4)
        for i in range(N):
            acc[i] += c[i]
    acc = allpass(allpass(acc, 556 + spread), 441 + spread)
    rooms.append(acc)

L, R = [0.0] * N, [0.0] * N
for i in range(N):
    dry = keys[i] + pad[i] + drums[i]
    L[i] = dry + 0.06 * rooms[0][i]
    R[i] = dry + 0.06 * rooms[1][i]

# fade in the first 30 ms and out over the last 1.5 s, soft clip, peak normalize;
# build.sh brings it to -16 LUFS
fi, fo = int(0.03 * SR), int(1.5 * SR)
for i in range(N):
    g = min(1, i / fi) * min(1, (N - i) / fo)
    L[i] = math.tanh(1.2 * L[i] * g)
    R[i] = math.tanh(1.2 * R[i] * g)
peak = max(max(abs(v) for v in L), max(abs(v) for v in R)) or 1.0
g = 0.9 / peak
with wave.open(sys.argv[1], 'wb') as w:
    w.setnchannels(2)
    w.setsampwidth(2)
    w.setframerate(SR)
    w.writeframes(b''.join(struct.pack('<hh', int(L[i] * g * 32767), int(R[i] * g * 32767)) for i in range(N)))
