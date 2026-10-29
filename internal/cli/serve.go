package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nick5616/canon/api"
	"github.com/nick5616/canon/ui"
)

func newServe() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the local UI and API on 127.0.0.1",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, path, err := loadConfig(cmd)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if cmd.Flags().Changed("port") {
				cfg.Serve.Port = port
			}
			if ps := cfg.Validate(); len(ps) > 0 {
				return fmt.Errorf("%s: %v", path, ps)
			}

			token, err := newToken()
			if err != nil {
				return err
			}
			addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Serve.Port))
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			in, err := newIngester(cfg)
			if err != nil {
				return err
			}
			eng, err := newEngine(cfg)
			if err != nil {
				return err
			}
			srv := &http.Server{
				Handler:           api.NewServer(cfg, token, ui.Dist, api.WithIngester(in), api.WithEngine(eng)),
				ReadHeaderTimeout: 10 * time.Second,
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			go func() {
				<-ctx.Done()
				shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdown)
			}()

			fmt.Fprintf(cmd.OutOrStdout(), "canon serving on http://%s/#token=%s\n", addr, token)
			if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "port to listen on (default from config, else 4747)")
	return cmd
}

// newToken returns 256 random bits, URL-safe so it survives the fragment.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
