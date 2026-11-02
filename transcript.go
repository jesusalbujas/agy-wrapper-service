package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func (app *application) transcriptPath(conversationID string) string {
	conversationID = filepath.Base(filepath.Clean(conversationID))
	return filepath.Join(app.geminiDir, "antigravity-cli", "brain", conversationID, ".system_generated", "logs", "transcript.jsonl")
}

func (app *application) getThinkingFromTranscript(conversationID string) *string {
	if conversationID == "" {
		return nil
	}
	file, err := os.Open(app.transcriptPath(conversationID))
	if err != nil {
		return nil
	}
	defer file.Close()
	thoughts := make([]string, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxCommandOutput)
	for scanner.Scan() {
		var step map[string]any
		if json.Unmarshal(scanner.Bytes(), &step) == nil {
			if thought := strings.TrimSpace(stringValue(step["thinking"])); thought != "" {
				thoughts = append(thoughts, thought)
			}
		}
	}
	if len(thoughts) == 0 {
		return nil
	}
	joined := strings.Join(thoughts, "\n\n---\n\n")
	return &joined
}

func (app *application) getToolActionsFromTranscript(conversationID string) []any {
	tools := make([]any, 0)
	if conversationID == "" {
		return tools
	}
	file, err := os.Open(app.transcriptPath(conversationID))
	if err != nil {
		return tools
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxCommandOutput)
	for scanner.Scan() {
		var step map[string]any
		if json.Unmarshal(scanner.Bytes(), &step) != nil {
			continue
		}
		calls, _ := step["tool_calls"].([]any)
		for _, call := range calls {
			if toolCall, ok := call.(map[string]any); ok {
				tools = append(tools, map[string]any{"name": toolCall["name"], "args": toolCall["args"]})
			}
		}
	}
	return tools
}
