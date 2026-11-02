package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func (app *application) resolveOptions(body map[string]any, query map[string][]string) requestOptions {
	getQuery := func(key string) string {
		if values := query[key]; len(values) > 0 {
			return values[0]
		}
		return ""
	}
	model := firstNonEmpty(stringValue(body["model"]), getQuery("model"), app.defaultModel)
	level := firstNonEmpty(stringValue(body["level"]), stringValue(body["effort"]), getQuery("level"), getQuery("effort"))
	effort := normalizeLevel(level)
	if effort == "" && !strings.HasSuffix(model, "-low") && !strings.HasSuffix(model, "-medium") && !strings.HasSuffix(model, "-high") && !strings.HasPrefix(strings.ToLower(model), "claude") {
		effort = normalizeLevel(app.defaultLevel)
		if effort == "" {
			effort = "medium"
		}
	}
	conversationID := firstNonEmpty(stringValue(body["conversation_id"]), stringValue(body["conversation"]), getQuery("conversation_id"))
	return requestOptions{
		Model:          model,
		Effort:         effort,
		WorkspaceDir:   firstNonEmpty(stringValue(body["workspaceDir"]), defaultWorkspace),
		Continue:       boolValue(body["continue"]) || boolValue(body["c"]) || getQuery("continue") == "true",
		ConversationID: conversationID,
	}
}

func normalizeLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "none", "off", "0":
		return ""
	case "low", "1":
		return "low"
	case "medium", "med", "2":
		return "medium"
	case "high", "3":
		return "high"
	default:
		return strings.ToLower(strings.TrimSpace(level))
	}
}

func (app *application) executeAgy(ctx context.Context, query string, opts requestOptions) (commandResult, error) {
	result, nativeErr := app.runNativeAgy(ctx, query, opts, "json")
	if nativeErr == nil {
		return result, nil
	}
	log.Printf("native agy failed (%v); trying the companion-for-agy fallback", nativeErr)
	return app.runCompanionFallback(ctx, query, opts)
}

func (app *application) runNativeAgy(ctx context.Context, query string, opts requestOptions, outputFormat string) (commandResult, error) {
	args := []string{"--dangerously-skip-permissions", "--add-dir", opts.WorkspaceDir, "--output-format", outputFormat}
	if opts.ConversationID != "" {
		args = append(args, "--conversation", opts.ConversationID)
	} else if opts.Continue {
		args = append(args, "-c")
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
	}
	args = append(args, "-p", query)

	cmd := exec.CommandContext(ctx, app.agyPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return commandResult{}, fmt.Errorf("%s", message)
	}

	var meta map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output), &meta); err != nil {
		return commandResult{Response: strings.TrimSpace(string(output)), Tools: []any{}}, nil
	}
	conversationID := stringValue(meta["conversation_id"])
	thinking := app.getThinkingFromTranscript(conversationID)
	tools := app.getToolActionsFromTranscript(conversationID)
	return commandResult{
		Response: strings.TrimSpace(stringValue(meta["response"])),
		Thinking: thinking,
		Tools:    tools,
		Meta:     meta,
	}, nil
}

func (app *application) runCompanionFallback(ctx context.Context, query string, opts requestOptions) (commandResult, error) {
	args := []string{"companion-for-agy", "--json", "--skip-permissions", "--add-dir", opts.WorkspaceDir, "--model", valueOr(opts.Model, defaultModel)}
	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
	}
	args = append(args, query)
	cmd := exec.CommandContext(ctx, "npx", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return commandResult{}, fmt.Errorf("companion-for-agy failed: %s", message)
	}
	var meta map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output), &meta); err != nil {
		return commandResult{Response: strings.TrimSpace(string(output)), Tools: []any{}}, nil
	}
	return commandResult{Response: strings.TrimSpace(stringValue(meta["response"])), Meta: meta, Tools: []any{}}, nil
}

func (app *application) formatResponse(w http.ResponseWriter, r *http.Request, body map[string]any, result commandResult, opts requestOptions) {
	conversationID := ""
	if result.Meta != nil {
		conversationID = stringValue(result.Meta["conversation_id"])
	}
	if conversationID != "" {
		w.Header().Set("X-Conversation-Id", conversationID)
	}
	if result.Tools == nil {
		result.Tools = []any{}
	}
	showThinking := boolValue(body["thinking"]) || r.URL.Query().Get("thinking") == "true" || os.Getenv("SHOW_THINKING") == "true"
	wantsJSON := boolValue(body["json"]) || r.URL.Query().Get("json") == "true" || strings.Contains(r.Header.Get("Accept"), "application/json")
	if wantsJSON {
		writeJSON(w, http.StatusOK, map[string]any{"response": result.Response, "thinking": result.Thinking, "tools": result.Tools, "conversation_id": nullableString(conversationID)})
		return
	}
	if boolValue(body["full"]) || r.URL.Query().Get("full") == "true" {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true, "response": result.Response, "thinking": result.Thinking, "tools": result.Tools,
			"model": opts.Model, "level": nullableString(opts.Effort), "conversation_id": nullableString(conversationID), "meta": result.Meta,
		})
		return
	}
	response := result.Response
	if showThinking && result.Thinking != nil && strings.TrimSpace(*result.Thinking) != "" {
		response = "💭 [Thoughts]:\n" + *result.Thinking + "\n\n" + response
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, response)
}

func (app *application) streamAgy(w http.ResponseWriter, r *http.Request, query string, opts requestOptions, forceSSE bool) {
	isSSE := wantsSSE(r) || forceSSE
	if isSSE {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}

	args := []string{"--dangerously-skip-permissions", "--add-dir", opts.WorkspaceDir, "--output-format", "stream-json"}
	if opts.ConversationID != "" {
		args = append(args, "--conversation", opts.ConversationID)
	} else if opts.Continue {
		args = append(args, "-c")
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
	}
	args = append(args, "-p", query)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, app.agyPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to start agy", "details": err.Error()})
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to start agy", "details": err.Error()})
		return
	}
	if err := cmd.Start(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to start agy", "details": err.Error()})
		return
	}

	lineChannel := make(chan string, 64)
	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		defer close(lineChannel)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), maxCommandOutput)
		for scanner.Scan() {
			select {
			case lineChannel <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			log.Printf("agy output stream: %v", err)
		}
	}()
	go logCommandStderr(stderr)
	waitChannel := make(chan error, 1)

	var convID = opts.ConversationID
	var emittedThinking = make(map[int]struct{})
	var ticker *time.Ticker
	var tickerChannel <-chan time.Time
	wroteResponse := false
	lines := lineChannel
	var commandErr error
	waited := false
	waitStarted := false
	defer func() {
		cancel()
		if waitStarted {
			if !waited {
				<-waitChannel
			}
			return
		}
		if stdoutDone != nil {
			<-stdoutDone
		}
		_ = cmd.Wait()
	}()

	flush := func() {
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	writeChunk := func(chunk string) bool {
		if _, err := io.WriteString(w, chunk); err != nil {
			cancel()
			return false
		}
		wroteResponse = true
		flush()
		return true
	}
	writeSSE := func(value any) bool {
		encoded, err := json.Marshal(value)
		if err != nil {
			return true
		}
		return writeChunk("data: " + string(encoded) + "\n\n")
	}
	checkTranscript := func() bool {
		if convID == "" {
			return true
		}
		path := app.transcriptPath(convID)
		file, err := os.Open(path)
		if err != nil {
			return true
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), maxCommandOutput)
		index := 0
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				if _, exists := emittedThinking[index]; !exists {
					var step map[string]any
					if json.Unmarshal([]byte(line), &step) == nil {
						if thought := strings.TrimSpace(stringValue(step["thinking"])); thought != "" {
							emittedThinking[index] = struct{}{}
							if isSSE {
								if !writeSSE(map[string]any{"type": "thinking", "text": thought}) {
									return false
								}
							} else if !writeChunk("\n💭 [Thoughts]:\n" + thought + "\n\n") {
								return false
							}
						}
					}
				}
				index++
			}
		}
		return true
	}

	for lines != nil || !waited {
		select {
		case <-r.Context().Done():
			cancel()
			return
		case line, ok := <-lines:
			if !ok {
				lines = nil
				continue
			}
			var event map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &event) != nil {
				continue
			}
			switch stringValue(event["event"]) {
			case "init":
				if id := stringValue(event["conversation_id"]); id != "" {
					convID = id
					w.Header().Set("X-Conversation-Id", convID)
					if ticker == nil {
						ticker = time.NewTicker(80 * time.Millisecond)
						tickerChannel = ticker.C
					}
				}
			case "step_update":
				step, _ := event["step_update"].(map[string]any)
				if step == nil {
					continue
				}
				stepType := stringValue(step["step_type"])
				if stepType == "tool" && stringValue(step["state"]) == "ACTIVE" {
					toolName := firstNonEmpty(stringValue(step["tool_name"]), nestedString(step, "tool_info", "name"), "tool")
					toolInfo, _ := step["tool_info"].(map[string]any)
					var params any
					if toolInfo != nil {
						params = toolInfo["parameters"]
					}
					if isSSE {
						if !writeSSE(map[string]any{"type": "tool", "tool": toolName, "params": params}) {
							return
						}
					} else {
						paramStr := ""
						if values, ok := params.(map[string]any); ok && stringValue(values["CommandLine"]) != "" {
							paramStr = " -> " + stringValue(values["CommandLine"])
						}
						if !writeChunk(fmt.Sprintf("\n⚡ [Action: %s%s]\n", toolName, paramStr)) {
							return
						}
					}
				} else if stepType == "agent_response" && stringValue(step["text_delta"]) != "" {
					delta := stringValue(step["text_delta"])
					if isSSE {
						if !writeSSE(map[string]any{"type": "text", "text": delta}) {
							return
						}
					} else if !writeChunk(delta) {
						return
					}
				}
			case "result":
				if !checkTranscript() {
					return
				}
				if isSSE && !writeChunk("data: [DONE]\n\n") {
					return
				}
			}
		case <-tickerChannel:
			if !checkTranscript() {
				return
			}
		case err := <-waitChannel:
			commandErr = err
			waited = true
			waitChannel = nil
		case <-stdoutDone:
			stdoutDone = nil
			if !waited {
				waitStarted = true
				go func() { waitChannel <- cmd.Wait() }()
			}
		}
	}
	if ticker != nil {
		ticker.Stop()
	}
	if !checkTranscript() {
		return
	}
	if commandErr != nil && !wroteResponse {
		var exitErr *exec.ExitError
		if errors.As(commandErr, &exitErr) {
			http.Error(w, fmt.Sprintf("agy failed (exit code %d)", exitErr.ExitCode()), http.StatusInternalServerError)
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to start agy", "details": commandErr.Error()})
		}
		return
	}
}

func logCommandStderr(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "Fetching available models") {
			log.Printf("[agy stderr] %s", line)
		}
	}
}
