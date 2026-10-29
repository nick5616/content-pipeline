// Package testmedia builds the synthetic media fixtures for tests, once per
// version of testdata/media/make-media.sh.
package testmedia

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// Planted are the private strings every fixture carries somewhere.
var Planted = []string{"SN-FAKE-0042", "Fake Owner", "FakeCam", "FakeSoft", "FakeCity", "37.7749"}

func root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// Dir returns the fixture directory, generating it if the script changed.
// Fixtures needing a missing tool are simply absent; see Need.
func Dir(t testing.TB) string {
	t.Helper()
	script := filepath.Join(root(), "testdata", "media", "make-media.sh")
	out := filepath.Join(root(), "testdata", "_out", "media")
	b, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	stamp := hex.EncodeToString(sum[:])
	_, heif := exec.LookPath("heif-enc")
	if heif == nil {
		stamp += "+heif"
	}

	// Packages test in parallel; one builds, the others wait on the lock.
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(out+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	if got, _ := os.ReadFile(out + ".stamp"); string(got) == stamp {
		return out
	}
	cmd := exec.Command("bash", script, out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make-media.sh: %v\n%s", err, b)
	}
	if err := os.WriteFile(out+".stamp", []byte(stamp), 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

// Need skips the test when a tool is missing, with the install hint.
func Need(t testing.TB, tool, pkg string) {
	t.Helper()
	if _, err := exec.LookPath(tool); err != nil {
		t.Skipf("%s not installed (sudo apt-get install -y %s)", tool, pkg)
	}
}
