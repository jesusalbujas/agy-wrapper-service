package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func (app *application) handleAsk(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	body, err := decodeJSONBody(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	query := stringValue(body["query"])
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": `The "query" field is required`})
		return
	}

	opts := app.resolveOptions(body, r.URL.Query())
	if err := validateAgyOptions(opts); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	wantsStream := boolValue(body["stream"]) || r.URL.Query().Get("stream") == "true" || strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	log.Printf("/ask: %q | Model: %s | Effort: %s | Stream: %t | Continue: %t", truncate(query, 80), opts.Model, valueOr(opts.Effort, "default"), wantsStream, opts.Continue)
	if wantsStream {
		app.streamAgy(w, r, query, opts, boolValue(body["sse"]), wantsThinking(r, body))
		return
	}

	result, err := app.executeAgy(r.Context(), query, opts)
	if err != nil {
		log.Printf("error in /ask: %v", err)
		writeJSON(w, agyErrorStatus(err), map[string]any{
			"error":   "An error occurred while running the Antigravity CLI.",
			"details": err.Error(),
		})
		return
	}
	app.formatResponse(w, r, body, result, opts)
}

func (app *application) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
		return
	}
	var body map[string]any
	if r.Method == http.MethodPost {
		var err error
		body, err = decodeJSONBody(w, r)
		if err != nil {
			writePlainError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	query := r.URL.Query().Get("query")
	if r.Method == http.MethodPost {
		query = stringValue(body["query"])
	}
	if query == "" {
		field := `The "query" parameter is required`
		if r.Method == http.MethodPost {
			field = `The "query" field is required`
		}
		writePlainError(w, http.StatusBadRequest, field)
		return
	}
	opts := app.resolveOptions(body, r.URL.Query())
	if err := validateAgyOptions(opts); err != nil {
		writePlainError(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		opts.WorkspaceDir = defaultWorkspace
	}
	app.streamAgy(w, r, query, opts, boolValue(body["sse"]), wantsThinking(r, body))
}

func (app *application) handleAskFile(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBodyBytes)
	body, filePath, originalName, cleanup, err := parseFileRequest(w, r, app.uploadDir)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}

	instruction := stringValue(body["query"])
	if instruction == "" {
		instruction = stringValue(body["instruction"])
	}
	if instruction == "" {
		instruction = "Analyze this file in detail, explain what it does, and highlight potential issues or improvements."
	}
	opts := app.resolveOptions(body, r.URL.Query())
	opts.WorkspaceDir = defaultWorkspace
	if err := validateAgyOptions(opts); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	fullPrompt := fmt.Sprintf("Instruction: %s\nFile to inspect: %q (original name: %q).\nRead the file and respond to the instruction.", instruction, filePath, originalName)

	log.Printf("/ask-file: File %q | Instruction: %q", originalName, truncate(instruction, 60))
	if boolValue(body["stream"]) || r.URL.Query().Get("stream") == "true" || strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		app.streamAgy(w, r, fullPrompt, opts, boolValue(body["sse"]), wantsThinking(r, body))
		return
	}
	result, err := app.executeAgy(r.Context(), fullPrompt, opts)
	if err != nil {
		log.Printf("error in /ask-file: %v", err)
		writeJSON(w, agyErrorStatus(err), map[string]any{"error": "Failed to analyze the file", "details": err.Error()})
		return
	}
	app.formatResponse(w, r, body, result, opts)
}

func parseFileRequest(w http.ResponseWriter, r *http.Request, uploadDir string) (map[string]any, string, string, func(), error) {
	body := make(map[string]any)
	contentType := r.Header.Get("Content-Type")
	var cleanup func()
	var targetPath, originalName string

	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return nil, "", "", nil, fmt.Errorf("invalid multipart form: %w", err)
		}
		if r.MultipartForm != nil {
			cleanup = func() { _ = r.MultipartForm.RemoveAll() }
		}
		for _, key := range []string{"query", "instruction", "filePath", "model", "level", "effort", "stream", "json", "full", "thinking", "workspaceDir", "conversation_id", "conversation", "continue", "c"} {
			if value := r.FormValue(key); value != "" {
				body[key] = value
			}
		}
		file, header, err := r.FormFile("file")
		if err == nil {
			defer file.Close()
			originalName = header.Filename
			name := safeFilename.ReplaceAllString(filepath.Base(originalName), "_")
			temp, createErr := os.CreateTemp(uploadDir, fmt.Sprintf("%d-%s", time.Now().UnixMilli(), name))
			if createErr != nil {
				return nil, "", "", cleanup, fmt.Errorf("save uploaded file: %w", createErr)
			}
			targetPath = temp.Name()
			if _, copyErr := io.Copy(temp, file); copyErr != nil {
				temp.Close()
				os.Remove(targetPath)
				return nil, "", "", cleanup, fmt.Errorf("save uploaded file: %w", copyErr)
			}
			if closeErr := temp.Close(); closeErr != nil {
				os.Remove(targetPath)
				return nil, "", "", cleanup, fmt.Errorf("save uploaded file: %w", closeErr)
			}
		} else if !errors.Is(err, http.ErrMissingFile) {
			return nil, "", "", cleanup, fmt.Errorf("read uploaded file: %w", err)
		}
	} else if strings.HasPrefix(contentType, "application/json") {
		var err error
		body, err = decodeJSONBody(w, r)
		if err != nil {
			return nil, "", "", nil, err
		}
	} else {
		if err := r.ParseForm(); err != nil {
			return nil, "", "", nil, fmt.Errorf("invalid form: %w", err)
		}
		for key, values := range r.Form {
			if len(values) > 0 {
				body[key] = values[0]
			}
		}
	}

	if targetPath != "" {
		return body, targetPath, originalName, cleanup, nil
	}
	requestedPath := stringValue(body["filePath"])
	if requestedPath == "" {
		if cleanup != nil {
			cleanup()
		}
		return nil, "", "", nil, errors.New(`Attach a file ("file") or specify an existing path ("filePath")`)
	}
	if !filepath.IsAbs(requestedPath) {
		requestedPath = filepath.Join(defaultWorkspace, requestedPath)
	}
	info, err := os.Stat(requestedPath)
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", "", nil, fmt.Errorf("File not found: %s: %w", requestedPath, os.ErrNotExist)
		}
		return nil, "", "", nil, fmt.Errorf("File not found: %s", requestedPath)
	}
	if info.IsDir() {
		return nil, "", "", cleanup, fmt.Errorf("File path is a directory: %s", requestedPath)
	}
	return body, requestedPath, filepath.Base(requestedPath), cleanup, nil
}

func (app *application) handleModels(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, app.agyPath, "models")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to retrieve models", "details": strings.TrimSpace(stderr.String())})
		return
	}
	lines := strings.Split(string(output), "\n")
	models := make([]map[string]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(strings.ReplaceAll(line, "Fetching available models...", ""))
		if line == "" || strings.Contains(strings.ToLower(line), "fetching") {
			continue
		}
		parts := regexp.MustCompile(`\t+|\s{2,}`).Split(line, -1)
		id := strings.TrimSpace(parts[0])
		name := id
		if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
			name = strings.TrimSpace(parts[1])
		}
		models = append(models, map[string]string{"id": id, "name": name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"levels": []string{"low", "medium", "high"}, "models": models})
}

func (app *application) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"defaultModel": app.defaultModel,
		"defaultLevel": app.defaultLevel,
		"workspace":    defaultWorkspace,
		"uploadsDir":   app.uploadDir,
	})
}

func (app *application) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "running",
		"service": "agy-cli-wrapper",
		"endpoints": map[string]string{
			"ask":     "POST /ask (body: { query, model?, level?, stream?, json? })",
			"stream":  "GET|POST /ask/stream (query/body: { query, model?, level? })",
			"askFile": "POST /ask-file (multipart \"file\" or body: { filePath, query })",
			"models":  "GET /models",
			"health":  "GET /health",
		},
	})
}
