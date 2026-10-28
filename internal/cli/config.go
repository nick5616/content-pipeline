package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nick5616/canon/engine/config"
)

func newConfig() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Inspect the config file"}
	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the config path in use",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, path, _ := loadConfig(cmd)
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "env",
		Short: "Print the config as shell assignments (read by scripts/provision-buckets.sh)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, path, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			if ps := cfg.Validate(); len(ps) > 0 {
				return fmt.Errorf("%s: %v", path, ps)
			}
			for _, kv := range [][2]string{
				{"CANON_PROJECT", cfg.Project},
				{"CANON_REGION", cfg.Region},
				{"CANON_PRIVATE_BUCKET", cfg.PrivateBucket},
				{"CANON_PUBLIC_BUCKET", cfg.PublicBucket},
				{"CANON_ARCHIVE_BUCKET", cfg.ArchiveBucket},
				{"CANON_SERVICE_ACCOUNT", cfg.ImpersonateServiceAccount},
				{"CANON_OPERATOR", cfg.Operator},
				{"CANON_CORS_ORIGINS", strings.Join(cfg.PublicCORSOrigins, " ")},
			} {
				fmt.Fprintf(cmd.OutOrStdout(), "%s=%s\n", kv[0], shellQuote(kv[1]))
			}
			return nil
		},
	})
	return cmd
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// defaultsFor keeps whatever parsed and fills the rest from the environment.
func defaultsFor(cfg config.Config) config.Config {
	d := config.Default(os.Getenv)
	if cfg.CacheDir == "" {
		cfg.CacheDir = d.CacheDir
	}
	if cfg.StateDir == "" {
		cfg.StateDir = d.StateDir
	}
	if cfg.Serve.Port == 0 {
		cfg.Serve.Port = d.Serve.Port
	}
	return cfg
}
