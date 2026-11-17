package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
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
	mux.HandleFunc("/resume", app.handleResume)
	mux.HandleFunc("/ask/stream", app.handleStream)
	mux.HandleFunc("/ask-file", app.handleAskFile)
	mux.HandleFunc("/conversations", app.handleConversationList)
	mux.HandleFunc("/conversations/", app.handleConversationRoutes)
	mux.HandleFunc("/sessions/", app.handleSession)
	mux.HandleFunc("/models", app.handleModels)
	mux.HandleFunc("/agents", app.handleAgents)
	mux.HandleFunc("/plugins", app.handlePlugins)
	mux.HandleFunc("/skills", app.handleSkills)
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/", app.handleRoot)

	server := &http.Server{
		Addr:              ":" + app.port,
		Handler:           logRequests(withCORS(app, withAuth(app, mux))),
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

func withCORS(app *application, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(app.corsOrigins) > 0 {
			origin := r.Header.Get("Origin")
			allowedOrigin := ""
			for _, candidate := range app.corsOrigins {
				if candidate == "*" {
					allowedOrigin = "*"
					break
				}
				if origin != "" && strings.EqualFold(candidate, origin) {
					allowedOrigin = origin
					break
				}
			}
			if allowedOrigin != "" {
				w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
				if allowedOrigin != "*" {
					w.Header().Add("Vary", "Origin")
				}
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, Accept")
				w.Header().Set("Access-Control-Expose-Headers", "X-Conversation-Id, X-Conversation-Title, X-Conversation-Name-Error")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func withAuth(app *application, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.apiKey == "" || r.URL.Path == "/health" || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		token := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if token == "" {
			authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
			if len(authHeader) > 7 && strings.EqualFold(authHeader[:7], "Bearer ") {
				token = strings.TrimSpace(authHeader[7:])
			}
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(app.apiKey)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
