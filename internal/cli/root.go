// Package cli wires canon's subcommands. Each command lives in its own file
// and stays thin: parse flags, call the engine, print.
package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/nick5616/canon/engine/config"
)

// exitError carries a specific exit code without printing anything more.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "canon",
		Short:         "One manifest for everything Nicole publishes",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("config", "", "config file (default $XDG_CONFIG_HOME/canon/config.yaml)")
	root.AddCommand(newVersion(), newDoctor(), newServe(), newConfig(), newIngest())
	root.AddCommand(stubs()...)
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	err := newRoot().Execute()
	var code exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &code):
		return int(code)
	default:
		fmt.Fprintln(os.Stderr, "canon:", err)
		return 1
	}
}

// loadConfig loads the --config file, or the default path. A missing file
// returns defaults plus the not-exist error, so callers can decide whether
// that matters.
func loadConfig(cmd *cobra.Command) (config.Config, string, error) {
	path, _ := cmd.Flags().GetString("config")
	explicit := path != ""
	if !explicit {
		path = config.DefaultPath(os.Getenv)
	}
	cfg, err := config.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		if explicit {
			return config.Config{}, path, err
		}
		return config.Default(os.Getenv), path, err
	}
	return cfg, path, err
}
