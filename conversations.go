package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var annotationTitleLine = regexp.MustCompile(`^(\s*title\s*:\s*)("(?:\\.|[^"\\])*")(\s*(?:#.*)?$)`)

type conversationSummary struct {
	ID        string    `json:"conversation_id"`
	Name      string    `json:"name"`
	Preview   string    `json:"preview,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (app *application) handleConversationList(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	limit := 50
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "limit must be between 1 and 200"})
			return
		}
		limit = parsed
	}
	conversations, err := app.listConversations()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to list Antigravity conversations", "details": err.Error()})
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	includePreview := strings.EqualFold(r.URL.Query().Get("include_preview"), "true")
	filtered := make([]conversationSummary, 0, len(conversations))
	for _, conversation := range conversations {
		matches := query == "" || strings.Contains(strings.ToLower(conversation.ID), query) || strings.Contains(strings.ToLower(conversation.Name), query)
		if !matches && includePreview {
			matches = strings.Contains(strings.ToLower(conversation.Preview), query)
		}
		if matches {
			if !includePreview {
				conversation.Preview = ""
			}
			filtered = append(filtered, conversation)
		}
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": filtered, "count": len(filtered), "limit": limit})
}

func (app *application) handleResume(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	body, err := decodeJSONBody(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	title := firstNonEmpty(stringValue(body["title"]), stringValue(body["name"]))
	requestedID := strings.TrimSpace(stringValue(body["conversation_id"]))
	if title == "" && requestedID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": `provide a conversation "title" or "conversation_id"`})
		return
	}
	conversations, err := app.listConversations()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to search Antigravity conversations", "details": err.Error()})
		return
	}
	matches := make([]conversationSummary, 0, 1)
	if requestedID != "" {
		if !safeConversationID.MatchString(requestedID) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "conversation_id must be a UUID"})
			return
		}
		for _, conversation := range conversations {
			if conversation.ID == requestedID {
				matches = append(matches, conversation)
				break
			}
		}
	} else {
		for _, conversation := range conversations {
			if strings.EqualFold(strings.TrimSpace(conversation.Name), strings.TrimSpace(title)) {
				matches = append(matches, conversation)
			}
		}
	}
	if len(matches) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "No conversation matched that title or ID", "title": title})
		return
	}
	if len(matches) > 1 {
		candidates := make([]conversationSummary, 0, len(matches))
		for _, match := range matches {
			match.Preview = ""
			candidates = append(candidates, match)
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "More than one conversation has this title. Retry with the selected conversation_id.",
			"title":   title,
			"matches": candidates,
		})
		return
	}
	selected := matches[0]
	body["conversation_id"] = selected.ID
	delete(body, "title")
	delete(body, "name")
	query := strings.TrimSpace(stringValue(body["query"]))
	if query == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"conversation_id": selected.ID,
			"name":            selected.Name,
			"updated_at":      selected.UpdatedAt,
			"ready_to_resume": true,
			"message":         `Add a "query" field to continue this conversation.`,
		})
		return
	}
	opts := app.resolveOptions(body, r.URL.Query())
	if err := validateAgyOptions(opts); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	wantsStream := boolValue(body["stream"]) || r.URL.Query().Get("stream") == "true" || strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	if wantsStream {
		app.streamAgy(w, r, query, opts, boolValue(body["sse"]), wantsThinking(r, body))
		return
	}
	result, err := app.executeAgy(r.Context(), query, opts)
	if err != nil {
		log.Printf("resume conversation %s failed: %v", selected.ID, err)
		payload := map[string]any{
			"error":      "Failed to resume Antigravity conversation",
			"details":    err.Error(),
			"retryable":  true,
			"message":    "The conversation is still available. Continue using its conversation_id; avoid resending the identical prompt if it may already be in the conversation history.",
			"name":       selected.Name,
			"updated_at": selected.UpdatedAt,
		}
		if result.Meta != nil {
			if conversationID := stringValue(result.Meta["conversation_id"]); conversationID != "" {
				payload["conversation_id"] = conversationID
			}
		}
		writeJSON(w, agyErrorStatus(err), payload)
		return
	}
	app.formatResponse(w, r, body, result, opts)
}

func (app *application) listConversations() ([]conversationSummary, error) {
	baseDir := filepath.Join(app.geminiDir, "antigravity-cli")
	entriesByID := make(map[string]conversationSummary)
	for _, directory := range []string{filepath.Join(baseDir, "conversations"), filepath.Join(baseDir, "annotations")} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", directory, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			var conversationID string
			switch filepath.Ext(name) {
			case ".db", ".pb":
				conversationID = strings.TrimSuffix(name, filepath.Ext(name))
			case ".pbtxt":
				conversationID = strings.TrimSuffix(name, ".pbtxt")
			default:
				continue
			}
			if !safeConversationID.MatchString(conversationID) {
				continue
			}
			summary := entriesByID[conversationID]
			summary.ID = conversationID
			if summary.Name == "" {
				summary.Name = conversationID
			}
			info, err := entry.Info()
			if err == nil && info.ModTime().After(summary.UpdatedAt) {
				summary.UpdatedAt = info.ModTime()
			}
			if filepath.Ext(name) == ".pbtxt" {
				content, readErr := os.ReadFile(filepath.Join(directory, name))
				if readErr == nil {
					if title := annotationTitle(content); title != "" {
						summary.Name = title
					}
				}
			}
			if summary.Preview == "" {
				summary.Preview = app.conversationPreview(conversationID)
			}
			entriesByID[conversationID] = summary
		}
	}
	conversations := make([]conversationSummary, 0, len(entriesByID))
	for _, summary := range entriesByID {
		conversations = append(conversations, summary)
	}
	sort.Slice(conversations, func(i, j int) bool {
		if conversations[i].UpdatedAt.Equal(conversations[j].UpdatedAt) {
			return conversations[i].ID > conversations[j].ID
		}
		return conversations[i].UpdatedAt.After(conversations[j].UpdatedAt)
	})
	return conversations, nil
}

func (app *application) conversationPreview(conversationID string) string {
	file, err := os.Open(app.transcriptPath(conversationID))
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), maxCommandOutput)
	for scanner.Scan() {
		var step struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if json.Unmarshal(scanner.Bytes(), &step) != nil || step.Type != "USER_INPUT" {
			continue
		}
		preview := strings.Join(strings.Fields(step.Content), " ")
		runes := []rune(preview)
		if len(runes) > 180 {
			preview = string(runes[:177]) + "..."
		}
		return preview
	}
	return ""
}

func annotationTitle(content []byte) string {
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	scanner.Buffer(make([]byte, 4096), maxJSONBodyBytes)
	for scanner.Scan() {
		if match := annotationTitleLine.FindStringSubmatch(scanner.Text()); match != nil {
			title, err := strconv.Unquote(match[2])
			if err == nil {
				return title
			}
		}
	}
	return ""
}

func (app *application) handleConversationName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPatch {
		methodNotAllowed(w, http.MethodPut, http.MethodPatch)
		return
	}
	trimmedPath := strings.Trim(strings.TrimPrefix(r.URL.Path, "/conversations/"), "/")
	parts := strings.Split(trimmedPath, "/")
	if len(parts) != 2 || parts[1] != "name" {
		http.NotFound(w, r)
		return
	}
	body, err := decodeJSONBody(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	title := firstNonEmpty(stringValue(body["name"]), stringValue(body["title"]))
	if err := app.renameConversation(parts[0], title); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"conversation_id": parts[0],
		"name":            title,
		"renamed":         true,
	})
}

func (app *application) renameConversation(conversationID, title string) error {
	conversationID = strings.TrimSpace(conversationID)
	title = strings.TrimSpace(title)
	if !safeConversationID.MatchString(conversationID) {
		return errors.New("conversation_id must be a UUID")
	}
	if err := validateConversationTitle(title); err != nil {
		return err
	}

	app.annotationMu.Lock()
	defer app.annotationMu.Unlock()

	annotationDir := filepath.Join(app.geminiDir, "antigravity-cli", "annotations")
	annotationPath := filepath.Join(annotationDir, conversationID+".pbtxt")
	if !app.conversationExists(conversationID, annotationPath) {
		return fmt.Errorf("conversation %s was not found: %w", conversationID, os.ErrNotExist)
	}
	if err := os.MkdirAll(annotationDir, 0o750); err != nil {
		return fmt.Errorf("create Antigravity annotation directory: %w", err)
	}

	content, mode := []byte{}, os.FileMode(0o644)
	if existing, err := os.ReadFile(annotationPath); err == nil {
		content = existing
		if info, statErr := os.Stat(annotationPath); statErr == nil {
			mode = info.Mode().Perm()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read conversation annotation: %w", err)
	}
	updated, err := replaceAnnotationTitle(content, title)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(annotationDir, ".conversation-title-*")
	if err != nil {
		return fmt.Errorf("create temporary annotation: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set annotation permissions: %w", err)
	}
	if _, err := temp.Write(updated); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write conversation annotation: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync conversation annotation: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close conversation annotation: %w", err)
	}
	if err := os.Rename(tempPath, annotationPath); err != nil {
		return fmt.Errorf("save conversation name: %w", err)
	}
	return nil
}

func validateConversationTitle(title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New(`the "name" field is required`)
	}
	if len([]rune(title)) > 120 {
		return errors.New("conversation name must be 120 characters or fewer")
	}
	for _, character := range title {
		if unicode.IsControl(character) {
			return errors.New("conversation name cannot contain control characters")
		}
	}
	return nil
}

func (app *application) conversationExists(conversationID, annotationPath string) bool {
	if _, err := os.Stat(annotationPath); err == nil {
		return true
	}
	base := filepath.Join(app.geminiDir, "antigravity-cli", "conversations", conversationID)
	for _, extension := range []string{".db", ".pb"} {
		if _, err := os.Stat(base + extension); err == nil {
			return true
		}
	}
	_, err := os.Stat(app.transcriptPath(conversationID))
	return err == nil
}

func replaceAnnotationTitle(content []byte, title string) ([]byte, error) {
	quoted := strconv.Quote(title)
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	scanner.Buffer(make([]byte, 4096), maxJSONBodyBytes)
	lines := make([]string, 0)
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if match := annotationTitleLine.FindStringSubmatch(line); match != nil {
			if found {
				return nil, errors.New("conversation annotation contains multiple title fields")
			}
			line = match[1] + quoted + match[3]
			found = true
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read conversation annotation: %w", err)
	}
	if !found {
		lines = append(lines, "title:"+quoted)
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}
