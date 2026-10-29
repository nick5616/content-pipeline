package recipes

import (
	"context"

	"github.com/nick5616/canon/engine/recipes/djgrid"
)

// djgridRecipe is the DJ room's analysis (D15): beat grid, key and start
// into facts, the three-band waveform into analysis.json.
var djgridRecipe = Recipe{
	Name: "djgrid", Version: 1,
	Params:  map[string]any{"rate": djgrid.Rate, "waveFps": djgrid.WaveFPS, "port": "build-dj-crate.mjs@872f68a"},
	Applies: isAudio,
	FFmpeg:  true,
	Facts:   []string{"bpm", "firstBeat", "start", "key", "camelot"},
	Run: func(ctx context.Context, t Tools, s Source, scratch string) (Result, error) {
		pcm, err := decodeMono(ctx, t, s.Path, djgrid.Rate)
		if err != nil {
			return Result{}, err
		}
		a, err := djgrid.Analyze(pcm)
		if err != nil {
			return Result{}, err
		}
		return Result{
			Files: []File{{Name: "analysis.json", JSON: a.WaveJSON()}},
			Facts: map[string]any{"bpm": a.BPM, "firstBeat": a.FirstBeat, "start": a.Start, "key": a.Key, "camelot": a.Camelot},
		}, nil
	},
}
