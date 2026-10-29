// Package djgrid works out what a CDJ would show for a track: BPM and beat
// grid, key, where the sound starts, and a three-band waveform. It is a
// port of the analysis in 3d-portfolio-website's scripts/build-dj-crate.mjs
// (872f68a), kept arithmetic-for-arithmetic so both agree on the same audio:
// float32 where the script stores into a Float32Array, float64 everywhere
// else, and JavaScript's Math.round. testdata/djgrid/reference.mjs is that
// script's analysis, and the parity test runs both.
package djgrid

import (
	"encoding/base64"
	"fmt"
	"math"
)

// Rate is the sample rate the analysis expects (mono float PCM).
const Rate = 11025

// WaveFPS is waveform columns per second of audio.
const WaveFPS = 100

// Analysis is one track's result, rounded as crate.json stores it.
type Analysis struct {
	Duration  float64
	BPM       float64
	FirstBeat float64 // the first downbeat at or after Start
	Start     float64 // where the sound starts: the auto-cue point
	Key       string
	Camelot   string
	Waveform  []byte // 3 bands per column, 0..255
}

// Wave is analysis.json, in the DJ room's format.
type Wave struct {
	FPS      int    `json:"fps"`
	Bands    int    `json:"bands"`
	Waveform string `json:"waveform"` // base64
}

// WaveJSON is the waveform as the DJ room reads it.
func (a Analysis) WaveJSON() Wave {
	return Wave{FPS: WaveFPS, Bands: 3, Waveform: base64.StdEncoding.EncodeToString(a.Waveform)}
}

// jsRound is Math.round: halves go up, toward +Inf.
func jsRound(x float64) float64 { return math.Floor(x + 0.5) }

// Analyze runs the whole analysis on pcm at Rate.
func Analyze(pcm []float32) (Analysis, error) {
	if len(pcm) < Rate/2 {
		return Analysis{}, fmt.Errorf("djgrid: %d samples is too short to analyze", len(pcm))
	}
	duration := float64(len(pcm)) / Rate
	start := firstSound(pcm)
	bpm, firstBeat := tempo(pcm)
	beat := 60 / bpm
	grid := firstBeat + math.Max(0, math.Ceil((start-firstBeat-beat*0.25)/beat))*beat
	k, camelot := key(pcm)
	return Analysis{
		Duration:  jsRound(duration*1000) / 1000,
		BPM:       bpm,
		FirstBeat: jsRound(grid*10000) / 10000,
		Start:     jsRound(start*1000) / 1000,
		Key:       k,
		Camelot:   camelot,
		Waveform:  waveform(pcm),
	}, nil
}

func onePole(cutoff float64) func(float64) float64 {
	a := math.Exp((-2 * math.Pi * cutoff) / Rate)
	y := 0.0
	return func(x float64) float64 {
		y = x + a*(y-x)
		return y
	}
}

// waveform: three bands (low < 200 Hz, mid to 2.5 kHz, high above), peak
// per column, 0..255 on a gentle curve.
func waveform(pcm []float32) []byte {
	lowPass := onePole(200)
	midPass := onePole(2500)
	hop := float64(Rate) / WaveFPS
	columns := int(math.Ceil(float64(len(pcm)) / hop))
	var bands [3][]float32
	for b := range bands {
		bands[b] = make([]float32, columns)
	}
	for i, xs := range pcm {
		x := float64(xs)
		low := lowPass(x)
		belowHigh := midPass(x)
		c := int(math.Floor(float64(i) / hop))
		bands[0][c] = float32(math.Max(float64(bands[0][c]), math.Abs(low)))
		bands[1][c] = float32(math.Max(float64(bands[1][c]), math.Abs(belowHigh-low)))
		bands[2][c] = float32(math.Max(float64(bands[2][c]), math.Abs(x-belowHigh)))
	}
	out := make([]byte, columns*3)
	for b, band := range bands {
		peak := 1e-6
		for _, v := range band {
			peak = math.Max(peak, float64(v))
		}
		for c := 0; c < columns; c++ {
			out[c*3+b] = byte(jsRound(math.Pow(float64(band[c])/peak, 0.7) * 255))
		}
	}
	return out
}

const (
	onsetHop = 64
	onsetFPS = float64(Rate) / onsetHop
)

// onsets: rises in (mostly low-end) energy, about 172 frames a second, with
// a running mean taken away so steady loudness doesn't count.
func onsets(pcm []float32) []float32 {
	lowPass := onePole(150)
	frames := len(pcm) / onsetHop
	env := make([]float32, frames)
	for f := 0; f < frames; f++ {
		full, low := 0.0, 0.0
		for k := 0; k < onsetHop; k++ {
			x := float64(pcm[f*onsetHop+k])
			l := lowPass(x)
			full += x * x
			low += l * l
		}
		env[f] = float32(math.Log1p(1000 * (full + 3*low)))
	}
	flux := make([]float32, frames)
	for f := 1; f < frames; f++ {
		flux[f] = float32(math.Max(0, float64(env[f])-float64(env[f-1])))
	}
	out := make([]float32, frames)
	sum := 0.0
	const w = 32
	for f := 0; f < frames; f++ {
		prev := 0.0
		if f >= w {
			prev = float64(flux[f-w])
		}
		sum += float64(flux[f]) - prev
		out[f] = float32(math.Max(0, float64(flux[f])-sum/float64(min(f+1, w))))
	}
	return out
}

func at(signal []float32, x float64) float64 {
	i := int(math.Floor(x))
	if i < 0 || i+1 >= len(signal) {
		return 0
	}
	return float64(signal[i]) + (float64(signal[i+1])-float64(signal[i]))*(x-float64(i))
}

// combScore: how much onset sits on a tempo and phase's beats, plus a
// little on the off-beats.
func combScore(signal []float32, bpm, phase float64) float64 {
	period := (60 / bpm) * onsetFPS
	score := 0.0
	for x := phase; x < float64(len(signal)); x += period {
		score += at(signal, x) + 0.25*at(signal, x+period/2)
	}
	return score
}

func bestPhase(signal []float32, bpm float64) (phase, score float64) {
	period := (60 / bpm) * onsetFPS
	phase, score = 0, -1
	for p := 0.0; p < period; p += 0.5 {
		if s := combScore(signal, bpm, p); s > score {
			phase, score = p, s
		}
	}
	return phase, score
}

func tempo(pcm []float32) (bpm, firstBeat float64) {
	signal := onsets(pcm)
	n := float64(len(signal))
	// Coarse: autocorrelation over 70..180 BPM, nudged toward 125.
	coarse, coarseScore := 120.0, -1.0
	for b := 70.0; b <= 180; b += 0.5 {
		lag := (60 / b) * onsetFPS
		s := 0.0
		for f := 0.0; f+lag*4 < n; f += 2 {
			s += float64(signal[int(f)]) * (at(signal, f+lag) + 0.5*at(signal, f+lag*2) + 0.25*at(signal, f+lag*4))
		}
		s *= math.Exp(-0.5 * math.Pow(math.Log2(b/125)/0.9, 2))
		if s > coarseScore {
			coarse, coarseScore = b, s
		}
	}
	// Fine: the comb over the whole track, phase included. The second
	// loop's bound moves with best, as in the script.
	best, bestPh, bestScore := coarse, 0.0, -1.0
	for b := coarse - 1; b <= coarse+1; b += 0.05 {
		if p, s := bestPhase(signal, b); s > bestScore {
			best, bestPh, bestScore = b, p, s
		}
	}
	for b := best - 0.05; b <= best+0.05; b += 0.01 {
		if p, s := bestPhase(signal, b); s > bestScore {
			best, bestPh, bestScore = b, p, s
		}
	}
	// Tracks made in a DAW sit on a whole BPM; snap if that close.
	whole := jsRound(best)
	if math.Abs(best-whole) < 0.3 {
		p, _ := bestPhase(signal, whole)
		return whole, p / onsetFPS
	}
	return jsRound(best*100) / 100, bestPh / onsetFPS
}

func fft(re, im []float64) {
	n := len(re)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	for length := 2; length <= n; length <<= 1 {
		angle := (-2 * math.Pi) / float64(length)
		for i := 0; i < n; i += length {
			for k := 0; k < length/2; k++ {
				wr := math.Cos(angle * float64(k))
				wi := math.Sin(angle * float64(k))
				a := i + k
				b := a + length/2
				tr := re[b]*wr - im[b]*wi
				ti := re[b]*wi + im[b]*wr
				re[b] = re[a] - tr
				im[b] = im[a] - ti
				re[a] += tr
				im[a] += ti
			}
		}
	}
}

// Krumhansl-Kessler key profiles.
var (
	major = [12]float64{6.35, 2.23, 3.48, 2.33, 4.38, 4.09, 2.52, 5.19, 2.39, 3.66, 2.29, 2.88}
	minor = [12]float64{6.33, 2.68, 3.52, 5.38, 2.6, 3.53, 2.54, 4.75, 3.98, 2.69, 3.34, 3.17}
	notes = [12]string{"C", "C#", "D", "Eb", "E", "F", "F#", "G", "Ab", "A", "Bb", "B"}
	// Camelot wheel numbers, by tonic (C = 0).
	camelotMajor = [12]int{8, 3, 10, 5, 12, 7, 2, 9, 4, 11, 6, 1}
	camelotMinor = [12]int{5, 12, 7, 2, 9, 4, 11, 6, 1, 8, 3, 10}
)

func correlate(a, b [12]float64) float64 {
	mean := func(v [12]float64) float64 {
		s := 0.0
		for _, x := range v {
			s += x
		}
		return s / 12
	}
	ma, mb := mean(a), mean(b)
	num, da, db := 0.0, 0.0, 0.0
	for i := range a {
		num += (a[i] - ma) * (b[i] - mb)
		da += (a[i] - ma) * (a[i] - ma)
		db += (b[i] - mb) * (b[i] - mb)
	}
	d := da * db
	if d == 0 {
		d = 1
	}
	return num / math.Sqrt(d)
}

// key: a chromagram against the key profiles.
func key(pcm []float32) (name, camelot string) {
	const n = 4096
	var chroma [12]float64
	window := make([]float64, n)
	for i := range window {
		window[i] = 0.5 - 0.5*math.Cos((2*math.Pi*float64(i))/(n-1))
	}
	re, im := make([]float64, n), make([]float64, n)
	for start := 0; start+n < len(pcm); start += n {
		for i := 0; i < n; i++ {
			re[i] = float64(pcm[start+i]) * window[i]
			im[i] = 0
		}
		fft(re, im)
		for bin := 1; bin < n/2; bin++ {
			freq := (float64(bin) * Rate) / n
			if freq < 60 || freq > 2000 {
				continue
			}
			pitch := int(jsRound(12 * math.Log2(freq/261.63)))
			chroma[((pitch%12)+12)%12] += math.Sqrt(re[bin]*re[bin] + im[bin]*im[bin])
		}
	}
	bestScore, tonic, isMinor := -2.0, 0, false
	for t := 0; t < 12; t++ {
		var rotated [12]float64
		for i := range rotated {
			rotated[i] = chroma[(i+t)%12]
		}
		if s := correlate(rotated, major); s > bestScore {
			bestScore, tonic, isMinor = s, t, false
		}
		if s := correlate(rotated, minor); s > bestScore {
			bestScore, tonic, isMinor = s, t, true
		}
	}
	if isMinor {
		return notes[tonic] + "m", fmt.Sprintf("%dA", camelotMinor[tonic])
	}
	return notes[tonic], fmt.Sprintf("%dB", camelotMajor[tonic])
}

// firstSound: where the sound starts (about -40 dB).
func firstSound(pcm []float32) float64 {
	for i, x := range pcm {
		if math.Abs(float64(x)) > 0.01 {
			return math.Max(0, float64(i)/Rate-0.005)
		}
	}
	return 0
}
