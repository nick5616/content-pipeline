package publish

import (
	"path"
	"sort"
	"strings"

	"github.com/nick5616/canon/engine/derived"
	"github.com/nick5616/canon/engine/jobs"
	"github.com/nick5616/canon/engine/manifest"
	"github.com/nick5616/canon/engine/media"
	"github.com/nick5616/canon/engine/recipes"
	"github.com/nick5616/canon/engine/views"
)

// Receipts finds a finished job's receipt and reads its files: from the
// local derived cache, or from the private bucket for jobs another machine
// ran. ok is false when the job hasn't finished anywhere.
type Receipts interface {
	Receipt(jobkey string) (derived.Receipt, bool)
	File(jobkey, name string) ([]byte, error)
}

// JobMedia resolves views' assets from the jobs the manifest needs (M4).
type JobMedia struct {
	byBlob   map[string]map[string]jobs.Spec // blob hash -> recipe name -> spec
	types    map[string]media.Type
	receipts Receipts
}

// NewJobMedia indexes m's jobs. typeOf resolves blob types (local cache or
// bucket listing), as the planner does.
func NewJobMedia(m *manifest.Manifest, typeOf jobs.TypeOf, r Receipts) *JobMedia {
	jm := &JobMedia{byBlob: map[string]map[string]jobs.Spec{}, types: map[string]media.Type{}, receipts: r}
	for _, s := range jobs.Desired(m, recipes.All(), typeOf) {
		if jm.byBlob[s.Source] == nil {
			jm.byBlob[s.Source] = map[string]jobs.Spec{}
		}
		jm.byBlob[s.Source][s.Recipe.Name] = s
		jm.types[s.Source] = s.Type
	}
	return jm
}

func (jm *JobMedia) receipt(blob, recipe string) (jobs.Spec, derived.Receipt, bool) {
	s, ok := jm.byBlob[strings.TrimPrefix(blob, "sha256:")][recipe]
	if !ok {
		return jobs.Spec{}, derived.Receipt{}, false
	}
	rc, ok := jm.receipts.Receipt(s.Key)
	return s, rc, ok
}

func (jm *JobMedia) sizes(blob, recipe string) (views.Image, bool) {
	s, rc, ok := jm.receipt(blob, recipe)
	if !ok {
		return views.Image{}, false
	}
	var img views.Image
	for _, f := range rc.Files {
		if f.Width == 0 || f.Height == 0 {
			continue // a receipt from before thumb@v2 recorded sizes
		}
		img.Sizes = append(img.Sizes, views.Size{Path: jobs.FilePath(s.Key, f.Name), Width: f.Width, Height: f.Height})
	}
	if len(img.Sizes) == 0 {
		return views.Image{}, false
	}
	sort.Slice(img.Sizes, func(i, j int) bool { return img.Sizes[i].Width < img.Sizes[j].Width })
	return img, true
}

func (jm *JobMedia) Image(blob string) (views.Image, bool) {
	img, ok := jm.sizes(blob, "thumb")
	if !ok {
		return img, false
	}
	if s, _, ok := jm.receipt(blob, "lqip"); ok {
		if b, err := jm.receipts.File(s.Key, "lqip.webp"); err == nil {
			img.LQIP = b
		}
	}
	return img, true
}

func (jm *JobMedia) Poster(blob string) (views.Image, bool) { return jm.sizes(blob, "poster") }

func (jm *JobMedia) Track(blob string) (views.Track, bool) {
	sum := strings.TrimPrefix(blob, "sha256:")
	g, _, ok := jm.receipt(blob, "djgrid")
	if !ok {
		return views.Track{}, false
	}
	t := views.Track{Analysis: jobs.FilePath(g.Key, "analysis.json")}
	if s, _, ok := jm.receipt(blob, "dj-mp3"); ok {
		t.Audio = jobs.FilePath(s.Key, "track.mp3")
	} else if _, wanted := jm.byBlob[sum]["dj-mp3"]; wanted {
		return views.Track{}, false // a lossless source whose MP3 isn't ready
	} else if jm.types[sum].Name == media.MP3.Name {
		t.Audio = BlobPath(sum, media.MP3.Ext) // played as it is (D49)
	} else {
		return views.Track{}, false
	}
	return t, true
}

// BlobPath is a blob's object name (the same in both buckets).
func BlobPath(sum, ext string) string {
	return path.Join("blobs", "sha256", sum[0:2], sum[2:4], sum+"."+ext)
}
