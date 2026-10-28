package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	sa       = "canon-apply@my-proj.iam.gserviceaccount.com"
	operator = "user:me@example.com"
)

var flags = []string{
	"--project", "my-proj", "--region", "us-central1",
	"--private-bucket", "canon-priv", "--public-bucket", "canon-pub", "--archive-bucket", "canon-arch",
	"--service-account", sa, "--operator", operator,
	"--cors-origin", "https://a.example", "--cors-origin", "https://b.example",
}

// provision runs the script against the stub gcloud and returns its output,
// exit code and the gcloud calls it made.
func provision(t *testing.T, state string, args ...string) (string, int, []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	stub, _ := filepath.Abs("testdata")

	cmd := exec.Command("bash", append([]string{"provision-buckets.sh"}, args...)...)
	cmd.Env = append(os.Environ(),
		"PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"),
		"XDG_CONFIG_HOME="+dir, // no real config is picked up
		"STUB_LOG="+log, "STUB_STATE="+state, "STUB_SA="+sa, "STUB_OPERATOR="+operator,
		"STUB_PUBLIC=canon-pub", "STUB_ARCHIVE=canon-arch",
	)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	calls := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(b) == 0 {
		calls = nil
	}
	return string(out), code, calls
}

func mutating(calls []string) []string {
	var out []string
	for _, c := range calls {
		f := strings.Fields(c)
		if len(f) >= 3 && slices.Contains([]string{"create", "update", "add-iam-policy-binding"}, f[2]) {
			out = append(out, c)
		}
	}
	return out
}

func TestFirstRunCreatesEverything(t *testing.T) {
	out, code, calls := provision(t, "none", append(flags, "--apply")...)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	m := strings.Join(mutating(calls), "\n")
	for _, want := range []string{
		"iam service-accounts create canon-apply --project=my-proj",
		"iam service-accounts add-iam-policy-binding " + sa + " --project=my-proj --member=" + operator + " --role=roles/iam.serviceAccountTokenCreator",
		"storage buckets create gs://canon-priv --project=my-proj --location=us-central1 --uniform-bucket-level-access --public-access-prevention --soft-delete-duration=30d",
		"storage buckets create gs://canon-pub --project=my-proj --location=us-central1 --uniform-bucket-level-access --no-public-access-prevention --soft-delete-duration=30d",
		"storage buckets create gs://canon-arch --project=my-proj --location=us-central1 --uniform-bucket-level-access --public-access-prevention --soft-delete-duration=30d",
		"storage buckets update gs://canon-priv --versioning",
		"storage buckets update gs://canon-arch --versioning",
		"storage buckets add-iam-policy-binding gs://canon-pub --member=allUsers --role=roles/storage.objectViewer",
		"storage buckets add-iam-policy-binding gs://canon-priv --member=serviceAccount:" + sa + " --role=roles/storage.objectAdmin",
		"storage buckets add-iam-policy-binding gs://canon-pub --member=serviceAccount:" + sa + " --role=roles/storage.objectAdmin",
		"storage buckets add-iam-policy-binding gs://canon-arch --member=serviceAccount:" + sa + " --role=roles/storage.objectCreator",
		"storage buckets add-iam-policy-binding gs://canon-arch --member=serviceAccount:" + sa + " --role=roles/storage.objectViewer",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("missing call: %s", want)
		}
	}
	if !strings.Contains(m, "storage buckets update gs://canon-pub --versioning --cors-file=") {
		t.Errorf("public bucket should get versioning and CORS in one update")
	}
	// The archive is the only bucket where the account may not be objectAdmin.
	if strings.Contains(m, "gs://canon-arch --member=serviceAccount:"+sa+" --role=roles/storage.objectAdmin") {
		t.Error("archive must not grant objectAdmin")
	}
	if strings.Contains(strings.Join(calls, "\n"), "artandmedia") {
		t.Error("touched artandmedia")
	}
}

func TestDryRunMutatesNothing(t *testing.T) {
	out, code, calls := provision(t, "none", flags...)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if m := mutating(calls); len(m) != 0 {
		t.Fatalf("dry run made changes: %v", m)
	}
	if !strings.Contains(out, "would run: gcloud storage buckets create gs://canon-priv") || !strings.Contains(out, "--apply") {
		t.Fatalf("dry run should print the plan:\n%s", out)
	}
}

func TestSecondRunIsNoop(t *testing.T) {
	for _, mode := range [][]string{nil, {"--apply"}} {
		out, code, calls := provision(t, "all", append(slices.Clone(flags), mode...)...)
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if m := mutating(calls); len(m) != 0 {
			t.Fatalf("%v: want zero mutating calls, got %v\n%s", mode, m, out)
		}
		if !strings.Contains(out, "No changes") {
			t.Fatalf("should say so:\n%s", out)
		}
	}
}

func TestDriftFixesOnlyWhatDiffers(t *testing.T) {
	out, code, calls := provision(t, "drift", append(flags, "--apply")...)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	m := mutating(calls)
	if len(m) != 2 {
		t.Fatalf("want 2 changes, got %v", m)
	}
	if !strings.HasPrefix(m[0], "storage buckets update gs://canon-pub --versioning --cors-file=") {
		t.Errorf("got %s", m[0])
	}
	if m[1] != "storage buckets add-iam-policy-binding gs://canon-arch --member=serviceAccount:"+sa+" --role=roles/storage.objectViewer" {
		t.Errorf("got %s", m[1])
	}
}

func TestRefusesBeforeAnyCall(t *testing.T) {
	cases := map[string][]string{
		"nothing set":      nil,
		"no operator":      slices.Delete(slices.Clone(flags), 12, 14),
		"import source":    append(slices.Clone(flags), "--public-bucket", "artandmedia"),
		"same bucket":      append(slices.Clone(flags), "--archive-bucket", "canon-priv"),
		"sa other project": append(slices.Clone(flags), "--service-account", "canon-apply@other-proj.iam.gserviceaccount.com"),
		"unknown flag":     append(slices.Clone(flags), "--yolo"),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			out, code, calls := provision(t, "none", append(args, "--apply")...)
			if code != 2 {
				t.Fatalf("want exit 2, got %d\n%s", code, out)
			}
			if len(calls) != 0 {
				t.Fatalf("called gcloud: %v", calls)
			}
		})
	}
}
