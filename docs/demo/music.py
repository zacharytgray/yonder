# soundtrack for the demo, synthesized from scratch. stdlib only.
# usage: python3 music.py out.wav
# 84 bpm in F major, one chord a bar: Fmaj9, Am7, Bbmaj9, C9sus4, Dm9, Bbmaj9 to C9sus4,
# then Fmaj9 held under the outro. soft electric piano chords on 1 and the and of 2,
# a low pad, a felt thump on 1 and 3 once the scenes start, and a small room.
# the outro starts on the final chord, at 6 bars: index.html uses the same BAR.
# build.sh sets the loudness.
import math
import random
import struct
import sys
import wave

SR = 44100
BPM = 84
BEAT = 60 / BPM
BAR = 4 * BEAT
DUR = 21.6
N = int(SR * DUR)
random.seed(7)

keys = [0.0] * N
pad = [0.0] * N
thump = [0.0] * N


def hz(note):
    names = {'C': -9, 'D': -7, 'E': -5, 'F': -4, 'G': -2, 'A': 0, 'B': 2}
    n = names[note[0]]
    rest = note[1:]
    if rest.startswith('b'):
        n -= 1
        rest = rest[1:]
    return 440.0 * 2 ** ((n + (int(rest) - 4) * 12) / 12)


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


def epiano(t0, note, dur, amp):
    # two-operator fm at ratio 1, brightness fading in half a second, plus a soft tine
    f = hz(note)
    i0, n = int(t0 * SR), int(dur * SR)
    rel = int(0.35 * SR)
    w = 2 * math.pi * f / SR
    for i in range(n):
        j = i0 + i
        if j >= N:
            break
        x = i / SR
        idx = 0.5 + 1.6 * math.exp(-x / 0.35)
        env = min(1, x / 0.005) * (0.55 * math.exp(-x / 0.5) + 0.45 * math.exp(-x / 3.0))
        if i > n - rel:
            env *= (n - i) / rel
        trem = 1 + 0.06 * math.sin(2 * math.pi * 4.2 * x)
        v = math.sin(w * i + idx * math.sin(w * i))
        # the tine: a quiet partial near the 7th harmonic that dies in a tenth of a second
        v += 0.3 * math.exp(-x / 0.5) * math.sin(3 * w * i) + 0.22 * math.exp(-x / 0.07) * math.sin(6.97 * w * i)
        keys[j] += amp * env * trem * v


def pad_chord(t0, notes, dur, amp):
    # three slightly detuned sines per note, slow swell in and out
    i0, n = int(t0 * SR), int(dur * SR)
    att, rel = int(0.9 * SR), int(1.0 * SR)
    for note in notes:
        f = hz(note)
        ws = [2 * math.pi * f * d / SR for d in (0.997, 1.0, 1.003)]
        ph = [random.uniform(0, 6.28) for _ in ws]
        for i in range(n):
            j = i0 + i
            if j >= N:
                break
            env = min(1, i / att) * min(1, (n - i) / rel)
            v = (math.sin(ws[0] * i + ph[0]) + math.sin(ws[1] * i + ph[1]) + math.sin(ws[2] * i + ph[2])) / 3
            pad[j] += amp * env * v


def felt(t0, amp):
    # a soft low thump, like a felt beater on a floor tom
    i0, n = int(t0 * SR), int(0.4 * SR)
    ph = 0.0
    for i in range(n):
        j = i0 + i
        if j >= N:
            break
        x = i / SR
        f = 52 + 38 * math.exp(-x / 0.03)
        ph += 2 * math.pi * f / SR
        thump[j] += amp * min(1, x / 0.004) * math.exp(-x / 0.14) * math.sin(ph)


# (start in bars, length in bars, piano voicing, pad notes)
F9 = (['A3', 'C4', 'E4', 'G4'], ['F2', 'C3', 'A3'])
AM7 = (['G3', 'C4', 'E4', 'A4'], ['A2', 'E3', 'C4'])
BB9 = (['A3', 'C4', 'D4', 'F4'], ['Bb2', 'F3', 'D4'])
C9S = (['G3', 'Bb3', 'D4', 'F4'], ['C3', 'G3', 'F3'])
DM9 = (['F3', 'A3', 'C4', 'E4'], ['D3', 'A3', 'F3'])
PROG = [(0, 1, F9), (1, 1, AM7), (2, 1, BB9), (3, 1, C9S), (4, 1, DM9), (5, 0.5, BB9), (5.5, 0.5, C9S)]
END = 6  # the final chord, under the outro

for start, length, (voicing, pad_notes) in PROG:
    t0 = start * BAR
    pad_chord(t0, pad_notes, length * BAR + 0.9, 0.04)
    # the chord on 1, rolled a little; a lighter one on the and of 2 in full bars
    for k, nt in enumerate(voicing):
        epiano(t0 + k * 0.014, nt, length * BAR + 0.2, 0.06)
    if length == 1:
        for k, nt in enumerate(voicing[1:]):
            epiano(t0 + 1.5 * BEAT + k * 0.01, nt, 2.5 * BEAT, 0.032)
    # the pulse starts with the first scene, at bar 1
    if start >= 1:
        for b in (0, 2):
            if b * BEAT < length * BAR:
                felt(t0 + b * BEAT, 0.1 if b == 0 else 0.07)

t0 = END * BAR
pad_chord(t0, ['F2', 'C3', 'A3'], DUR - t0, 0.04)
for k, nt in enumerate(['F3', 'A3', 'C4', 'E4', 'G4']):
    epiano(t0 + k * 0.022, nt, DUR - t0, 0.06)
felt(t0, 0.09)

lowpass(keys, 4500)
lowpass(pad, 900)
lowpass(thump, 180)
highpass(pad, 70)


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


# a small dark room on the keys and pad, different combs left and right
send = [keys[i] + 0.5 * pad[i] for i in range(N)]
lowpass(send, 3000, 1)
rooms = []
for spread in (0, 23):
    acc = [0.0] * N
    for d in (1116, 1188, 1277, 1356):
        c = comb(send, d + spread, 0.78, 0.4)
        for i in range(N):
            acc[i] += c[i]
    acc = allpass(allpass(acc, 556 + spread), 441 + spread)
    rooms.append(acc)

L, R = [0.0] * N, [0.0] * N
for i in range(N):
    dry = keys[i] + pad[i] + thump[i]
    L[i] = dry + 0.055 * rooms[0][i]
    R[i] = dry + 0.055 * rooms[1][i]

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
