// Command go-exam serves the exam application.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata" // GOEXAM_TIME_ZONE works without host zoneinfo

	"github.com/fiatcode-gh/go-exam/internal/adminauth"
	"github.com/fiatcode-gh/go-exam/internal/attempt"
	"github.com/fiatcode-gh/go-exam/internal/config"
	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/platform/clientip"
	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite"
	"github.com/fiatcode-gh/go-exam/internal/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "go-exam:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	mediaDir := filepath.Join(cfg.DataDir, "media")
	if err := os.MkdirAll(mediaDir, 0o750); err != nil {
		return err
	}
	db, err := sqlite.Open(filepath.Join(cfg.DataDir, "go-exam.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	media, err := os.OpenRoot(mediaDir)
	if err != nil {
		return err
	}
	defer media.Close()

	admin, err := newAdmin(context.Background(), db, cfg.AdminPassword)
	if err != nil {
		return err
	}
	attempts := attempt.NewService(db, time.Now, rand.Reader)
	handler, err := web.NewHandler(web.Deps{
		DB:       db,
		Exams:    exam.NewService(db, media, time.Now, rand.Reader),
		Attempts: attempts,
		Admin:    admin,
		ClientIP: clientip.New(cfg.TrustedProxies),
		Location: cfg.Location,
		Logger:   logger,
	})
	if err != nil {
		return err
	}

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sweeperDone := make(chan struct{})
	go func() {
		runSweeper(ctx, sweepInterval, attempts.Sweep, logger)
		close(sweeperDone)
	}()
	// Runs before the deferred db.Close, so no sweep outlives the database.
	defer func() {
		stop()
		<-sweeperDone
	}()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	logger.Info("listening", "addr", ln.Addr().String(), "data_dir", cfg.DataDir)

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// newAdmin returns the teacher sign-in service. Admin sessions are not
// carried across a restart, so restarting with a changed password ends every
// session signed in with the old one.
func newAdmin(ctx context.Context, db *sql.DB, password string) (*adminauth.Service, error) {
	admin := adminauth.NewService(db, password, time.Now, rand.Reader)
	if err := admin.ClearSessions(ctx); err != nil {
		return nil, fmt.Errorf("clear admin sessions: %w", err)
	}
	return admin, nil
}

const (
	sweepInterval = 5 * time.Second
	sweepTimeout  = 5 * time.Second
)

// runSweeper calls sweep every interval, each call bounded by sweepTimeout,
// so an abandoned attempt times out with no browser connected. It returns
// when ctx is cancelled; sweep errors are logged and the loop goes on.
func runSweeper(ctx context.Context, every time.Duration, sweep func(context.Context) error, logger *slog.Logger) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		sweepCtx, cancel := context.WithTimeout(ctx, sweepTimeout)
		err := sweep(sweepCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Error("sweep failed", "err", err)
		}
	}
}
