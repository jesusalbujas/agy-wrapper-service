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
	result, nativeErr := app.runAgyTurn(ctx, query, opts, nil)
	if nativeErr == nil {
		return result, nil
	}
	return app.fallbackIfAgyMissing(ctx, query, opts, nativeErr)
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
	if showThinking && result.Thinking == nil && conversationID != "" {
		result.Thinking = app.getThinkingFromTranscript(conversationID)
	}
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

func (app *application) streamAgy(w http.ResponseWriter, r *http.Request, query string, opts requestOptions, forceSSE bool, showThinking bool) {
	isSSE := wantsSSE(r) || forceSSE
	if isSSE {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}

	flush := func() {
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	wroteResponse := false
	writeChunk := func(chunk string) bool {
		if _, err := io.WriteString(w, chunk); err != nil {
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
	conversationID := opts.ConversationID
	if conversationID != "" {
		w.Header().Set("X-Conversation-Id", conversationID)
	}
	onEvent := func(event map[string]any) error {
		switch stringValue(event["event"]) {
		case "init":
			if id := stringValue(event["conversation_id"]); id != "" {
				conversationID = id
				w.Header().Set("X-Conversation-Id", id)
			}
		case "step_update":
			step, _ := event["step_update"].(map[string]any)
			if step == nil {
				return nil
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
						return io.ErrClosedPipe
					}
				} else {
					paramStr := ""
					if values, ok := params.(map[string]any); ok && stringValue(values["CommandLine"]) != "" {
						paramStr = " -> " + stringValue(values["CommandLine"])
					}
					if !writeChunk(fmt.Sprintf("\n⚡ [Action: %s%s]\n", toolName, paramStr)) {
						return io.ErrClosedPipe
					}
				}
			} else if stepType == "agent_response" && stringValue(step["text_delta"]) != "" {
				delta := stringValue(step["text_delta"])
				if isSSE {
					if !writeSSE(map[string]any{"type": "text", "text": delta}) {
						return io.ErrClosedPipe
					}
				} else if !writeChunk(delta) {
					return io.ErrClosedPipe
				}
			}
		}
		return nil
	}

	result, err := app.runAgyTurn(r.Context(), query, opts, onEvent)
	if errors.Is(err, exec.ErrNotFound) {
		result, err = app.runCompanionFallback(r.Context(), query, opts)
	}
	if err != nil {
		if wroteResponse {
			if isSSE {
				_ = writeSSE(map[string]any{"type": "error", "error": err.Error()})
			} else {
				_ = writeChunk("\n[Antigravity error: " + err.Error() + "]\n")
			}
		} else {
			writeJSON(w, agyErrorStatus(err), map[string]any{"error": "Failed to run Antigravity CLI", "details": err.Error()})
		}
		return
	}
	if result.Meta != nil {
		if id := stringValue(result.Meta["conversation_id"]); id != "" {
			conversationID = id
			w.Header().Set("X-Conversation-Id", id)
		}
	}
	if !wroteResponse && result.Response != "" {
		if isSSE {
			if !writeSSE(map[string]any{"type": "text", "text": result.Response}) {
				return
			}
		} else if !writeChunk(result.Response) {
			return
		}
	}
	if showThinking && conversationID != "" {
		if thinking := app.getThinkingFromTranscript(conversationID); thinking != nil {
			if isSSE {
				if !writeSSE(map[string]any{"type": "thinking", "text": *thinking}) {
					return
				}
			} else if !writeChunk("\n💭 [Thoughts]:\n" + *thinking + "\n") {
				return
			}
		}
	}
	if isSSE {
		_ = writeChunk("data: [DONE]\n\n")
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
