package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/avelarfe/devices-api/internal/device"
	devicemysql "github.com/avelarfe/devices-api/internal/device/mysql"
	"github.com/avelarfe/devices-api/internal/id"
	"github.com/avelarfe/devices-api/internal/notify"

	_ "github.com/go-sql-driver/mysql"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	addr := getenv("HTTP_ADDR", ":8080")
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		slog.Error("DB_DSN is required")
		os.Exit(1)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		slog.Error("open database", "err", err)
		os.Exit(1)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	pingCtx, cancelPing := context.WithTimeout(context.Background(), 30*time.Second)
	err = waitForDB(pingCtx, db)
	cancelPing()
	if err != nil {
		slog.Error("database not ready", "err", err)
		os.Exit(1)
	}

	hub := notify.NewHub()
	service := device.NewService(devicemysql.NewRepository(db), hub.Publish, id.New)
	server := &http.Server{
		Addr:              addr,
		Handler:           logRequests(device.NewHandler(service, hub).Routes()),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		errCh <- server.ListenAndServe()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server stopped", "err", err)
			os.Exit(1)
		}
	case <-signals:
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			// An open event stream keeps the process busy until the deadline.
			// Close drops those connections so the container can exit.
			slog.Info("closing open connections", "err", err)
			_ = server.Close()
		}
	}
}

func waitForDB(ctx context.Context, db *sql.DB) error {
	var err error
	for {
		err = db.PingContext(ctx)
		if err == nil {
			return nil
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(wrapped, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", wrapped.status, "duration", time.Since(start))
	})
}
