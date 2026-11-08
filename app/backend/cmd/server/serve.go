package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// serve retains the listener failure after graceful HTTP/worker shutdown.
func serve(ctx context.Context, server *http.Server, stopWorker func(context.Context) error, logger *slog.Logger) error {
	errorsCh := make(chan error, 1)
	go func() { errorsCh <- server.ListenAndServe() }()
	logger.Info("Локальный учебный сервер запущен", "address", server.Addr, "mode", "demo")
	var listenErr error
	select {
	case <-ctx.Done():
	case err := <-errorsCh:
		if !errors.Is(err, http.ErrServerClosed) {
			listenErr = err
			logger.Error("HTTP-сервер остановлен")
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	serverErr := server.Shutdown(shutdown)
	queueErr := stopWorker(shutdown)
	return errors.Join(listenErr, serverErr, queueErr)
}
