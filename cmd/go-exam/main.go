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

	exams := exam.NewService(db, media, time.Now, rand.Reader)
	admin, err := newAdmin(context.Background(), db, exams, cfg.AdminEmails, logger)
	if err != nil {
		return err
	}
	if err := assignExamCodes(context.Background(), exams, logger); err != nil {
		return err
	}
	if err := backfillRevisions(context.Background(), exams, logger); err != nil {
		return err
	}
	attempts := attempt.NewService(db, time.Now, rand.Reader)
	handler, err := web.NewHandler(web.Deps{
		DB:       db,
		Exams:    exams,
		Attempts: attempts,
		Admin:    admin,
		OIDC: adminauth.NewProvider(adminauth.ProviderConfig{
			Issuer:       cfg.OIDCIssuer,
			ClientID:     cfg.GoogleClientID,
			ClientSecret: cfg.GoogleClientSecret,
			RedirectURL:  cfg.PublicURL + "/admin/auth/callback",
			HTTPClient:   &http.Client{Timeout: 10 * time.Second},
			Now:          time.Now,
			Random:       rand.Reader,
		}),
		ClientIP:  clientip.New(cfg.TrustedProxies),
		Location:  cfg.Location,
		Logger:    logger,
		PublicURL: cfg.PublicURL,
		Now:       time.Now,
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
	logger.Info("listening", "addr", ln.Addr().String(), "data_dir", cfg.DataDir, "oidc_issuer", cfg.OIDCIssuer)
	if cfg.OIDCIssuer != config.GoogleIssuer {
		logger.Warn("sign-in uses a non-Google OpenID provider", "oidc_issuer", cfg.OIDCIssuer)
	}

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

// newAdmin returns the sign-in service with every configured admin on the
// teacher list and gives exams that have no owner yet (those from before
// exam ownership) to the first admin. Sessions are kept across restarts.
func newAdmin(ctx context.Context, db *sql.DB, exams *exam.Service, adminEmails []string, logger *slog.Logger) (*adminauth.Service, error) {
	admin, err := adminauth.NewService(db, adminEmails, time.Now, rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("GOEXAM_ADMIN_EMAILS: %w", err)
	}
	firstAdminID, err := admin.EnsureAdmins(ctx)
	if err != nil {
		return nil, fmt.Errorf("add admins to the teacher list: %w", err)
	}
	n, err := exams.AssignUnowned(ctx, firstAdminID)
	if err != nil {
		return nil, fmt.Errorf("assign existing exams: %w", err)
	}
	if n > 0 {
		logger.Info("existing exams assigned to the first admin", "count", n)
	}
	return admin, nil
}

// assignExamCodes gives exams from before exam codes their code. Exams that
// have one are never touched, so later starts change nothing.
func assignExamCodes(ctx context.Context, exams *exam.Service, logger *slog.Logger) error {
	n, err := exams.AssignMissingCodes(ctx)
	if err != nil {
		return fmt.Errorf("give existing exams a code: %w", err)
	}
	if n > 0 {
		logger.Info("existing exams given a code", "count", n)
	}
	return nil
}

// backfillRevisions gives exams from before revisions their revision 1.
// Exams that have one are never touched, so later starts change nothing. An
// exam whose stored text cannot be rebuilt keeps working without a revision
// and is reported at every start.
func backfillRevisions(ctx context.Context, exams *exam.Service, logger *slog.Logger) error {
	created, skipped, err := exams.BackfillRevisions(ctx)
	if err != nil {
		return fmt.Errorf("give existing exams a revision: %w", err)
	}
	if created > 0 {
		logger.Info("existing exams given revision 1", "count", created)
	}
	for _, s := range skipped {
		logger.Error("exam has no revision: its stored text could not be rebuilt", "exam_id", s.ExamID, "err", s.Err)
	}
	return nil
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
