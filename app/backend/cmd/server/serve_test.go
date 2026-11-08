package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestServePreservesListenerFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	stopped := false
	err = serve(context.Background(), &http.Server{Addr: listener.Addr().String()},
		func(context.Context) error { stopped = true; return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "bind") {
		t.Fatalf("expected occupied address error, got %v", err)
	}
	if !stopped {
		t.Fatal("worker cleanup was skipped after listener failure")
	}
}

func TestServePreservesWorkerShutdownFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	shutdownErr := errors.New("worker shutdown failed")
	err := serve(ctx, &http.Server{Addr: "127.0.0.1:0"}, func(context.Context) error { return shutdownErr },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, shutdownErr) {
		t.Fatalf("worker error lost: %v", err)
	}
}
