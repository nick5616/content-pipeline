// The analysis half of 3d-portfolio-website's scripts/build-dj-crate.mjs at
// 872f68a, copied verbatim between the two marker lines, so the parity test
// in engine/recipes/djgrid can check the Go port against it. Don't edit the
// copied part; re-copy it if the script changes (and bump djgrid's version).
//
//   node reference.mjs <pcm.f32le>   mono float32 PCM at 11025 Hz -> JSON
import { readFile } from "node:fs/promises";

const run = () => {
    throw new Error("decodeMono is not used here: the PCM comes from a file");
};

// ---- copied: build-dj-crate.mjs lines 54-268 ----
// --- Analysis ----------------------------------------------------------------

const RATE = 11025; // plenty for beats, key and a waveform
const WAVE_FPS = 100; // waveform columns per second of audio

const decodeMono = async (path) => {
    const raw = await run("ffmpeg", ["-v", "error", "-i", path, "-ac", "1", "-ar", String(RATE), "-f", "f32le", "-"], { capture: true });
    return new Float32Array(raw.buffer, raw.byteOffset, raw.byteLength / 4);
};

const onePole = (cutoff) => {
    const a = Math.exp((-2 * Math.PI * cutoff) / RATE);
    let y = 0;
    return (x) => (y = x + a * (y - x));
};

// Three bands (low < 200 Hz, mid to 2.5 kHz, high above), peak per column,
// 0..255 on a gentle curve - the rekordbox-style coloured waveform.
const waveform = (pcm) => {
    const lowPass = onePole(200);
    const midPass = onePole(2500);
    const hop = RATE / WAVE_FPS;
    const columns = Math.ceil(pcm.length / hop);
    const bands = [new Float32Array(columns), new Float32Array(columns), new Float32Array(columns)];
    for (let i = 0; i < pcm.length; i++) {
        const x = pcm[i];
        const low = lowPass(x);
        const belowHigh = midPass(x);
        const c = Math.floor(i / hop);
        bands[0][c] = Math.max(bands[0][c], Math.abs(low));
        bands[1][c] = Math.max(bands[1][c], Math.abs(belowHigh - low));
        bands[2][c] = Math.max(bands[2][c], Math.abs(x - belowHigh));
    }
    const out = new Uint8Array(columns * 3);
    bands.forEach((band, b) => {
        let peak = 1e-6;
        band.forEach((v) => (peak = Math.max(peak, v)));
        for (let c = 0; c < columns; c++) out[c * 3 + b] = Math.round(Math.pow(band[c] / peak, 0.7) * 255);
    });
    return out;
};

// Onset strength: rises in (mostly low-end) energy, ~172 frames a second.
const ONSET_HOP = 64;
const ONSET_FPS = RATE / ONSET_HOP;
const onsets = (pcm) => {
    const lowPass = onePole(150);
    const frames = Math.floor(pcm.length / ONSET_HOP);
    const env = new Float32Array(frames);
    for (let f = 0; f < frames; f++) {
        let full = 0;
        let low = 0;
        for (let k = 0; k < ONSET_HOP; k++) {
            const x = pcm[f * ONSET_HOP + k];
            const l = lowPass(x);
            full += x * x;
            low += l * l;
        }
        env[f] = Math.log1p(1000 * (full + 3 * low));
    }
    const flux = new Float32Array(frames);
    for (let f = 1; f < frames; f++) flux[f] = Math.max(0, env[f] - env[f - 1]);
    // Take away a running mean so steady loudness doesn't count.
    const out = new Float32Array(frames);
    let sum = 0;
    const W = 32;
    for (let f = 0; f < frames; f++) {
        sum += flux[f] - (f >= W ? flux[f - W] : 0);
        out[f] = Math.max(0, flux[f] - sum / Math.min(f + 1, W));
    }
    return out;
};

const at = (signal, x) => {
    const i = Math.floor(x);
    if (i < 0 || i + 1 >= signal.length) return 0;
    return signal[i] + (signal[i + 1] - signal[i]) * (x - i);
};

// Score a tempo + phase by how much onset sits on its beats (and a little
// on the off-beats, which electronic music also stresses).
const combScore = (signal, bpm, phaseFrames) => {
    const period = (60 / bpm) * ONSET_FPS;
    let score = 0;
    for (let x = phaseFrames; x < signal.length; x += period) score += at(signal, x) + 0.25 * at(signal, x + period / 2);
    return score;
};

const bestPhase = (signal, bpm) => {
    const period = (60 / bpm) * ONSET_FPS;
    let best = { phase: 0, score: -1 };
    for (let p = 0; p < period; p += 0.5) {
        const score = combScore(signal, bpm, p);
        if (score > best.score) best = { phase: p, score };
    }
    return best;
};

const tempo = (pcm) => {
    const signal = onsets(pcm);
    // Coarse: autocorrelation over 70..180 BPM, nudged toward 120ish.
    let coarse = { bpm: 120, score: -1 };
    for (let bpm = 70; bpm <= 180; bpm += 0.5) {
        const lag = (60 / bpm) * ONSET_FPS;
        let s = 0;
        for (let f = 0; f + lag * 4 < signal.length; f += 2) s += signal[f] * (at(signal, f + lag) + 0.5 * at(signal, f + lag * 2) + 0.25 * at(signal, f + lag * 4));
        s *= Math.exp(-0.5 * Math.pow(Math.log2(bpm / 125) / 0.9, 2));
        if (s > coarse.score) coarse = { bpm, score: s };
    }
    // Fine: the comb over the whole track, 0.01 BPM steps, phase included.
    let best = { bpm: coarse.bpm, phase: 0, score: -1 };
    for (let bpm = coarse.bpm - 1; bpm <= coarse.bpm + 1; bpm += 0.05) {
        const p = bestPhase(signal, bpm);
        if (p.score > best.score) best = { bpm, phase: p.phase, score: p.score };
    }
    for (let bpm = best.bpm - 0.05; bpm <= best.bpm + 0.05; bpm += 0.01) {
        const p = bestPhase(signal, bpm);
        if (p.score > best.score) best = { bpm, phase: p.phase, score: p.score };
    }
    // Tracks made in a DAW sit on a whole BPM; snap if we're that close.
    const whole = Math.round(best.bpm);
    if (Math.abs(best.bpm - whole) < 0.3) return { bpm: whole, firstBeat: bestPhase(signal, whole).phase / ONSET_FPS };
    return { bpm: Math.round(best.bpm * 100) / 100, firstBeat: best.phase / ONSET_FPS };
};

// Key: a chromagram against the Krumhansl-Kessler profiles.
const fft = (re, im) => {
    const n = re.length;
    for (let i = 1, j = 0; i < n; i++) {
        let bit = n >> 1;
        for (; j & bit; bit >>= 1) j ^= bit;
        j ^= bit;
        if (i < j) {
            [re[i], re[j]] = [re[j], re[i]];
            [im[i], im[j]] = [im[j], im[i]];
        }
    }
    for (let len = 2; len <= n; len <<= 1) {
        const angle = (-2 * Math.PI) / len;
        for (let i = 0; i < n; i += len) {
            for (let k = 0; k < len / 2; k++) {
                const wr = Math.cos(angle * k);
                const wi = Math.sin(angle * k);
                const a = i + k;
                const b = a + len / 2;
                const tr = re[b] * wr - im[b] * wi;
                const ti = re[b] * wi + im[b] * wr;
                re[b] = re[a] - tr;
                im[b] = im[a] - ti;
                re[a] += tr;
                im[a] += ti;
            }
        }
    }
};
const MAJOR = [6.35, 2.23, 3.48, 2.33, 4.38, 4.09, 2.52, 5.19, 2.39, 3.66, 2.29, 2.88];
const MINOR = [6.33, 2.68, 3.52, 5.38, 2.6, 3.53, 2.54, 4.75, 3.98, 2.69, 3.34, 3.17];
const NOTES = ["C", "C#", "D", "Eb", "E", "F", "F#", "G", "Ab", "A", "Bb", "B"];
// Camelot wheel numbers, by tonic (C = 0).
const CAMELOT_MAJOR = [8, 3, 10, 5, 12, 7, 2, 9, 4, 11, 6, 1];
const CAMELOT_MINOR = [5, 12, 7, 2, 9, 4, 11, 6, 1, 8, 3, 10];

const correlate = (a, b) => {
    const mean = (v) => v.reduce((s, x) => s + x, 0) / v.length;
    const ma = mean(a);
    const mb = mean(b);
    let num = 0;
    let da = 0;
    let db = 0;
    for (let i = 0; i < a.length; i++) {
        num += (a[i] - ma) * (b[i] - mb);
        da += (a[i] - ma) ** 2;
        db += (b[i] - mb) ** 2;
    }
    return num / Math.sqrt(da * db || 1);
};

const key = (pcm) => {
    const N = 4096;
    const chroma = new Float64Array(12);
    const window = Float64Array.from({ length: N }, (_, i) => 0.5 - 0.5 * Math.cos((2 * Math.PI * i) / (N - 1)));
    const re = new Float64Array(N);
    const im = new Float64Array(N);
    for (let start = 0; start + N < pcm.length; start += N) {
        for (let i = 0; i < N; i++) {
            re[i] = pcm[start + i] * window[i];
            im[i] = 0;
        }
        fft(re, im);
        for (let bin = 1; bin < N / 2; bin++) {
            const freq = (bin * RATE) / N;
            if (freq < 60 || freq > 2000) continue;
            const pitch = Math.round(12 * Math.log2(freq / 261.63));
            chroma[((pitch % 12) + 12) % 12] += Math.sqrt(re[bin] ** 2 + im[bin] ** 2);
        }
    }
    let best = { score: -2, tonic: 0, minor: false };
    for (let tonic = 0; tonic < 12; tonic++) {
        const rotated = Array.from({ length: 12 }, (_, i) => chroma[(i + tonic) % 12]);
        const major = correlate(rotated, MAJOR);
        const minor = correlate(rotated, MINOR);
        if (major > best.score) best = { score: major, tonic, minor: false };
        if (minor > best.score) best = { score: minor, tonic, minor: true };
    }
    const name = NOTES[best.tonic] + (best.minor ? "m" : "");
    const camelot = `${(best.minor ? CAMELOT_MINOR : CAMELOT_MAJOR)[best.tonic]}${best.minor ? "A" : "B"}`;
    return { key: name, camelot };
};

// Where the sound starts (about -40 dB): the auto-cue point.
const firstSound = (pcm) => {
    for (let i = 0; i < pcm.length; i++) if (Math.abs(pcm[i]) > 0.01) return Math.max(0, i / RATE - 0.005);
    return 0;
};

// ---- end of copy ----

const raw = await readFile(process.argv[2]);
const pcm = new Float32Array(raw.buffer, raw.byteOffset, raw.byteLength / 4);
const duration = pcm.length / RATE;
const start = firstSound(pcm);
const { bpm, firstBeat } = tempo(pcm);
const beat = 60 / bpm;
const grid = firstBeat + Math.max(0, Math.ceil((start - firstBeat - beat * 0.25) / beat)) * beat;
const { key: musicalKey, camelot } = key(pcm);
const wave = waveform(pcm);
console.log(JSON.stringify({
    duration: Math.round(duration * 1000) / 1000,
    bpm,
    firstBeat: Math.round(grid * 10000) / 10000,
    start: Math.round(start * 1000) / 1000,
    key: musicalKey,
    camelot,
    analysis: JSON.stringify({ fps: WAVE_FPS, bands: 3, waveform: Buffer.from(wave).toString("base64") }),
}));
