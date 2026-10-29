package apply_test

import (
	"os"
	"testing"

	"github.com/nick5616/canon/internal/testenv"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testenv.Cleanup()
	os.Exit(code)
}
