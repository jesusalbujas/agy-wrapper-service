package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateAgyOptions(t *testing.T) {
	validUUID := "123e4567-e89b-12d3-a456-426614174000"

	if err := validateAgyOptions(requestOptions{ConversationID: validUUID, Agent: "research", Skills: []string{"smart-commit"}}); err != nil {
		t.Fatalf("expected valid options, got %v", err)
	}
	if err := validateAgyOptions(requestOptions{Continue: true}); err == nil {
		t.Fatal("expected error when continue=true without conversation_id")
	}
	if err := validateAgyOptions(requestOptions{ConversationID: "not-a-uuid"}); err == nil {
		t.Fatal("expected error for invalid conversation_id")
	}
	if err := validateAgyOptions(requestOptions{Agent: "bad agent!"}); err == nil {
		t.Fatal("expected error for invalid agent name")
	}
	tooManySkills := make([]string, 11)
	for i := range tooManySkills {
		tooManySkills[i] = "skill"
	}
	if err := validateAgyOptions(requestOptions{Skills: tooManySkills}); err == nil {
		t.Fatal("expected error when exceeding 10 skills")
	}
}

func TestParseAndApplySkills(t *testing.T) {
	skills := parseSkills([]any{"/smart-commit", "dory-tickets"}, "smart-commit, bad skill!")
	if len(skills) != 2 || skills[0] != "smart-commit" || skills[1] != "dory-tickets" {
		t.Fatalf("unexpected parsed skills: %v", skills)
	}
	prompt := applySkills("Review PR", skills)
	expected := "/smart-commit\n/dory-tickets\nReview PR"
	if prompt != expected {
		t.Fatalf("expected %q, got %q", expected, prompt)
	}
}

func TestAnnotationTitleHelpers(t *testing.T) {
	if err := validateConversationTitle("  "); err == nil {
		t.Fatal("expected error for empty title")
	}
	if err := validateConversationTitle("line1\nline2"); err == nil {
		t.Fatal("expected error for control character in title")
	}
	if err := validateConversationTitle(strings.Repeat("a", 121)); err == nil {
		t.Fatal("expected error for title > 120 runes")
	}

	raw := []byte("created_at: 123\ntitle: \"Old Title\"\n")
	updated, err := replaceAnnotationTitle(raw, `New "Title"`)
	if err != nil {
		t.Fatalf("replaceAnnotationTitle failed: %v", err)
	}
	if got := annotationTitle(updated); got != `New "Title"` {
		t.Fatalf("expected %q, got %q", `New "Title"`, got)
	}

	added, err := replaceAnnotationTitle([]byte("created_at: 123\n"), "First Title")
	if err != nil {
		t.Fatalf("replaceAnnotationTitle insert failed: %v", err)
	}
	if got := annotationTitle(added); got != "First Title" {
		t.Fatalf("expected %q, got %q", "First Title", got)
	}

	if _, err := replaceAnnotationTitle([]byte("title:\"A\"\ntitle:\"B\"\n"), "C"); err == nil {
		t.Fatal("expected error on duplicate title fields")
	}
}

func TestParseFileRequestCleanupAndPathBounds(t *testing.T) {
	tempRoot := t.TempDir()
	workspaceDir := filepath.Join(tempRoot, "workspace")
	uploadDir := filepath.Join(tempRoot, "uploads")
	outsideDir := filepath.Join(tempRoot, "outside")
	for _, dir := range []string{workspaceDir, uploadDir, outsideDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Multipart upload cleans up the created temp file.
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", "sample.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("hello world"))
	_ = writer.Close()

	req := httptest.NewRequest("POST", "/ask-file", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	_, targetPath, originalName, cleanup, err := parseFileRequest(rec, req, workspaceDir, uploadDir)
	if err != nil {
		t.Fatalf("multipart parseFileRequest failed: %v", err)
	}
	if originalName != "sample.txt" {
		t.Fatalf("expected originalName sample.txt, got %q", originalName)
	}
	if _, statErr := os.Stat(targetPath); statErr != nil {
		t.Fatalf("expected uploaded temp file to exist: %v", statErr)
	}
	cleanup()
	if _, statErr := os.Stat(targetPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected uploaded temp file to be removed by cleanup, statErr=%v", statErr)
	}

	// 2. JSON filePath inside workspace succeeds.
	insideFile := filepath.Join(workspaceDir, "notes.txt")
	if err := os.WriteFile(insideFile, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	jsonReq := httptest.NewRequest("POST", "/ask-file", strings.NewReader(`{"filePath":"notes.txt"}`))
	jsonReq.Header.Set("Content-Type", "application/json")
	_, resolvedPath, _, cleanup2, err := parseFileRequest(httptest.NewRecorder(), jsonReq, workspaceDir, uploadDir)
	if cleanup2 != nil {
		cleanup2()
	}
	if err != nil || resolvedPath != insideFile {
		t.Fatalf("expected %q, got %q (err=%v)", insideFile, resolvedPath, err)
	}

	// 3. JSON filePath outside workspace/uploads is rejected.
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	traversalReq := httptest.NewRequest("POST", "/ask-file", strings.NewReader(`{"filePath":"../outside/secret.txt"}`))
	traversalReq.Header.Set("Content-Type", "application/json")
	if _, _, _, _, err := parseFileRequest(httptest.NewRecorder(), traversalReq, workspaceDir, uploadDir); err == nil {
		t.Fatal("expected error for filePath outside workspace/uploads")
	}
}

func TestSessionManagerConcurrencyAndEviction(t *testing.T) {
	app := &application{}
	manager := newAgySessionManager(app, 1, time.Minute)
	id1 := "123e4567-e89b-12d3-a456-426614174001"
	id2 := "123e4567-e89b-12d3-a456-426614174002"

	s1, err := manager.acquire(id1, requestOptions{Model: defaultModel})
	if err != nil {
		t.Fatalf("acquire id1 failed: %v", err)
	}

	// Concurrent request for same conversation returns errAgySessionBusy.
	if _, err := manager.acquire(id1, requestOptions{Model: defaultModel}); !errors.Is(err, errAgySessionBusy) {
		t.Fatalf("expected errAgySessionBusy, got %v", err)
	}

	// Another conversation while maxSessions=1 and s1 is busy returns errAgySessionLimit.
	if _, err := manager.acquire(id2, requestOptions{Model: defaultModel}); !errors.Is(err, errAgySessionLimit) {
		t.Fatalf("expected errAgySessionLimit, got %v", err)
	}

	// Release s1; now id2 can evict idle s1.
	manager.release(s1)
	s2, err := manager.acquire(id2, requestOptions{Model: defaultModel})
	if err != nil {
		t.Fatalf("acquire id2 after release failed: %v", err)
	}
	manager.release(s2)
}

func TestAuthAndCORSMiddleware(t *testing.T) {
	app := &application{
		apiKey:      "super-secret-key",
		corsOrigins: []string{"http://localhost:5173"},
		startedAt:   time.Now(),
		maxSessions: 32,
	}
	handler := withCORS(app, withAuth(app, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})))

	// 1. Preflight OPTIONS succeeds without auth and sets CORS headers.
	optReq := httptest.NewRequest(http.MethodOptions, "/ask", nil)
	optReq.Header.Set("Origin", "http://localhost:5173")
	optRec := httptest.NewRecorder()
	handler.ServeHTTP(optRec, optReq)
	if optRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for OPTIONS, got %d", optRec.Code)
	}
	if got := optRec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("unexpected Allow-Origin: %q", got)
	}
	if got := optRec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-Conversation-Id") {
		t.Fatalf("expected X-Conversation-Id in Expose-Headers, got %q", got)
	}

	// 2. /health stays public.
	healthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthRec := httptest.NewRecorder()
	handler.ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for /health, got %d", healthRec.Code)
	}

	// 3. Protected endpoint rejects missing/invalid API key.
	unauthReq := httptest.NewRequest(http.MethodGet, "/models", nil)
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", unauthRec.Code)
	}

	// 4. Bearer and X-API-Key succeed.
	bearerReq := httptest.NewRequest(http.MethodGet, "/models", nil)
	bearerReq.Header.Set("Authorization", "Bearer super-secret-key")
	bearerRec := httptest.NewRecorder()
	handler.ServeHTTP(bearerRec, bearerReq)
	if bearerRec.Code != http.StatusOK {
		t.Fatalf("expected 200 with Bearer key, got %d", bearerRec.Code)
	}

	headerReq := httptest.NewRequest(http.MethodGet, "/models", nil)
	headerReq.Header.Set("X-API-Key", "super-secret-key")
	headerRec := httptest.NewRecorder()
	handler.ServeHTTP(headerRec, headerReq)
	if headerRec.Code != http.StatusOK {
		t.Fatalf("expected 200 with X-API-Key, got %d", headerRec.Code)
	}
}

func TestHandleConversationDetail(t *testing.T) {
	geminiDir := t.TempDir()
	convID := "123e4567-e89b-12d3-a456-426614174099"
	annDir := filepath.Join(geminiDir, "antigravity-cli", "annotations")
	logsDir := filepath.Join(geminiDir, "antigravity-cli", "brain", convID, ".system_generated", "logs")
	for _, dir := range []string{annDir, logsDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(annDir, convID+".pbtxt"), []byte("title: \"NexoInt Integration\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-17T10:00:00Z","content":"Hola"}`,
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-17T10:00:01Z","thinking":"step","tool_calls":[{"name":"read_file"}]}`,
		`{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-17T10:00:02Z","content":"Hola, ¿en qué ayudo?"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}

	app := &application{geminiDir: geminiDir}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/conversations/"+convID+"?limit=1", nil)
	app.handleConversationRoutes(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		ConversationID string                `json:"conversation_id"`
		Name           string                `json:"name"`
		Count          int                   `json:"count"`
		Messages       []conversationMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Name != "NexoInt Integration" || payload.Count != 1 || payload.Messages[0].Role != "assistant" || payload.Messages[0].Content != "Hola, ¿en qué ayudo?" {
		t.Fatalf("unexpected conversation detail payload: %+v", payload)
	}
}

func TestHandleSessionAndHealthMetrics(t *testing.T) {
	app := &application{
		defaultModel:       defaultModel,
		defaultLevel:       "medium",
		workspaceDir:       "/app",
		uploadDir:          "/app/uploads",
		apiKey:             "k",
		startedAt:          time.Now().Add(-5 * time.Second),
		maxSessions:        8,
		sessionIdleTimeout: 15 * time.Minute,
	}
	app.sessions = newAgySessionManager(app, app.maxSessions, app.sessionIdleTimeout)

	convID := "123e4567-e89b-12d3-a456-426614174010"
	session, err := app.sessions.acquire(convID, requestOptions{Model: defaultModel})
	if err != nil {
		t.Fatal(err)
	}

	// Health reflects activeSessions=1.
	hRec := httptest.NewRecorder()
	app.handleHealth(hRec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var health map[string]any
	_ = json.Unmarshal(hRec.Body.Bytes(), &health)
	if health["activeSessions"] != float64(1) || health["maxSessions"] != float64(8) || health["authEnabled"] != true {
		t.Fatalf("unexpected health payload: %+v", health)
	}

	// Busy session returns 409 on DELETE.
	busyRec := httptest.NewRecorder()
	app.handleSession(busyRec, httptest.NewRequest(http.MethodDelete, "/sessions/"+convID, nil))
	if busyRec.Code != http.StatusConflict {
		t.Fatalf("expected 409 when closing busy session, got %d", busyRec.Code)
	}

	// Release and DELETE -> 200 closed=true.
	app.sessions.release(session)
	delRec := httptest.NewRecorder()
	app.handleSession(delRec, httptest.NewRequest(http.MethodDelete, "/sessions/"+convID, nil))
	if delRec.Code != http.StatusOK {
		t.Fatalf("expected 200 when closing idle session, got %d: %s", delRec.Code, delRec.Body.String())
	}

	// Second DELETE -> 404.
	notFoundRec := httptest.NewRecorder()
	app.handleSession(notFoundRec, httptest.NewRequest(http.MethodDelete, "/sessions/"+convID, nil))
	if notFoundRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after session already closed, got %d", notFoundRec.Code)
	}
}
