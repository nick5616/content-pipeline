package importbucket

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Redirect maps one imported object to its item's published image.
type Redirect struct {
	Object string `json:"object"`
	Item   string `json:"item"`
	Slug   string `json:"slug"`
	URL    string `json:"url"`
}

// WriteRedirects writes <stateDir>/imports/<bucket>-urls.json beside each
// import report: every imported object whose item is published, with its
// new image URL (D43). Sites switch from the old bucket's URL for that
// object to this one. images maps item ids to their main image URL.
func WriteRedirects(stateDir string, images map[string]string) error {
	reports, err := filepath.Glob(filepath.Join(stateDir, "imports", "*.json"))
	if err != nil {
		return err
	}
	for _, path := range reports {
		if strings.HasSuffix(path, "-urls.json") {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var rep Report
		if err := json.Unmarshal(b, &rep); err != nil {
			return err
		}
		out := struct {
			Bucket    string     `json:"bucket"`
			Redirects []Redirect `json:"redirects"`
		}{Bucket: rep.Bucket, Redirects: []Redirect{}}
		for _, r := range rep.Results {
			if u, ok := images[r.Item]; ok && r.Item != "" {
				out.Redirects = append(out.Redirects, Redirect{Object: r.Object, Item: r.Item, Slug: r.Slug, URL: u})
			}
		}
		sort.Slice(out.Redirects, func(i, j int) bool { return out.Redirects[i].Object < out.Redirects[j].Object })
		ob, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		dst := strings.TrimSuffix(path, ".json") + "-urls.json"
		if err := os.WriteFile(dst+".tmp", append(ob, '\n'), 0o644); err != nil {
			return err
		}
		if err := os.Rename(dst+".tmp", dst); err != nil {
			return err
		}
	}
	return nil
}
