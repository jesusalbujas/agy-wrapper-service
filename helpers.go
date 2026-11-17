package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type upstreamAPIError struct {
	message string
}

func (err *upstreamAPIError) Error() string {
	return err.message
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var body map[string]any
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&body); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	if body == nil {
		body = make(map[string]any)
	}
	return body, nil
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		methodNotAllowed(w, method)
		return false
	}
	return true
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writePlainError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, message)
}

func wantsSSE(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/event-stream") || r.URL.Query().Get("sse") == "true"
}

func wantsThinking(r *http.Request, body map[string]any) bool {
	return boolValue(body["thinking"]) || r.URL.Query().Get("thinking") == "true" || os.Getenv("SHOW_THINKING") == "true"
}

func agyErrorStatus(err error) int {
	var upstreamErr *upstreamAPIError
	if errors.As(err, &upstreamErr) {
		return http.StatusBadGateway
	}
	switch {
	case errors.Is(err, errAgySessionBusy):
		return http.StatusConflict
	case errors.Is(err, errAgySessionLimit):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func validateAgyOptions(opts requestOptions) error {
	if opts.Continue && opts.ConversationID == "" {
		return errors.New("continuing a conversation requires its conversation_id")
	}
	if opts.ConversationID != "" && !safeConversationID.MatchString(opts.ConversationID) {
		return errors.New("conversation_id must be a UUID")
	}
	if opts.Title != "" {
		if err := validateConversationTitle(opts.Title); err != nil {
			return err
		}
	}
	if opts.Agent != "" && !safeCapabilityName.MatchString(opts.Agent) {
		return errors.New("agent must be a valid Antigravity agent name")
	}
	if len(opts.Skills) > 10 {
		return errors.New("a request can invoke at most 10 skills")
	}
	for _, skill := range opts.Skills {
		if !safeCapabilityName.MatchString(skill) {
			return errors.New("skills must be valid Antigravity skill names")
		}
	}
	return nil
}

func parseSkills(values ...any) []string {
	seen := make(map[string]struct{})
	skills := make([]string, 0)
	var add func(any)
	add = func(value any) {
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				add(item)
			}
		case []string:
			for _, item := range typed {
				add(item)
			}
		case string:
			for _, item := range strings.Split(typed, ",") {
				name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(item), "/"))
				if name == "" || !safeCapabilityName.MatchString(name) {
					continue
				}
				if _, exists := seen[name]; exists {
					continue
				}
				seen[name] = struct{}{}
				skills = append(skills, name)
			}
		}
	}
	for _, value := range values {
		add(value)
	}
	return skills
}

func applySkills(prompt string, skills []string) string {
	if len(skills) == 0 {
		return prompt
	}
	var prefix strings.Builder
	for _, skill := range skills {
		prefix.WriteByte('/')
		prefix.WriteString(skill)
		prefix.WriteByte('\n')
	}
	prefix.WriteString(prompt)
	return prefix.String()
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	case float64:
		return typed != 0
	default:
		return false
	}
}

func nestedString(value map[string]any, keys ...string) string {
	var current any = value
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[key]
	}
	return stringValue(current)
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func truncate(value string, length int) string {
	if len(value) <= length {
		return value
	}
	return value[:length] + "..."
}
