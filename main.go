package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	loadDotEnv(".env")
	configurePath()

	app := newApplication()
	app.sessions = newAgySessionManager(app, app.maxSessions, app.sessionIdleTimeout)
	if err := os.MkdirAll(app.uploadDir, 0o750); err != nil {
		log.Fatalf("create uploads directory: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ask", app.handleAsk)
	mux.HandleFunc("/ask/stream", app.handleStream)
	mux.HandleFunc("/ask-file", app.handleAskFile)
	mux.HandleFunc("/models", app.handleModels)
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/", app.handleRoot)

	server := &http.Server{
		Addr:              ":" + app.port,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	shutdownComplete := make(chan struct{})

	go func() {
		<-shutdownCtx.Done()
		defer close(shutdownComplete)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
			_ = server.Close()
		}
	}()
	go app.sessions.cleanupLoop(shutdownCtx)

	log.Printf("Antigravity CLI wrapper listening on port %s", app.port)
	log.Printf("Default model: %s", app.defaultModel)
	log.Printf("Uploads directory: %s", app.uploadDir)
	log.Print("API ready to receive requests.")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTP server failed: %v", err)
	}
	stop()
	<-shutdownComplete
	app.sessions.closeAll()
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(started).Round(time.Millisecond))
	})
}
