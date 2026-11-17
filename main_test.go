package main

import (
	"bytes"
	"errors"
	"mime/multipart"
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
