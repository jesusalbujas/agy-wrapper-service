package main

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	defaultModel       = "gemini-3.8-flash"
	defaultWorkspace   = "/app"
	maxJSONBodyBytes   = 10 << 20
	maxUploadBodyBytes = 100 << 20
	maxCommandOutput   = 20 << 20
)

var safeFilename = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

type application struct {
	port         string
	defaultModel string
	defaultLevel string
	agyPath      string
	uploadDir    string
	geminiDir    string
}

type requestOptions struct {
	Model          string
	Effort         string
	WorkspaceDir   string
	Continue       bool
	ConversationID string
}

type commandResult struct {
	Response string         `json:"response"`
	Thinking *string        `json:"thinking"`
	Tools    []any          `json:"tools"`
	Meta     map[string]any `json:"meta"`
}

func newApplication() *application {
	port := envOr("PORT", "3000")
	model := envOr("DEFAULT_MODEL", os.Getenv("AGY_MODEL"))
	if model == "" {
		model = defaultModel
	}
	level := envOr("DEFAULT_LEVEL", os.Getenv("DEFAULT_EFFORT"))
	if level == "" {
		level = "medium"
	}
	home := envOr("HOME", "/root")
	uploadDir := envOr("UPLOAD_DIR", filepath.Join("uploads"))
	if !filepath.IsAbs(uploadDir) {
		uploadDir = filepath.Join("/app", uploadDir)
	}
	geminiDir := envOr("GEMINI_DIR", filepath.Join(home, ".gemini"))
	agyPath := envOr("AGY_PATH", "/root/.local/bin/agy")
	if _, err := os.Stat(agyPath); err != nil {
		agyPath = "agy"
	}
	return &application{
		port:         port,
		defaultModel: model,
		defaultLevel: level,
		agyPath:      agyPath,
		uploadDir:    uploadDir,
		geminiDir:    geminiDir,
	}
}

func loadDotEnv(filename string) {
	file, err := os.Open(filename)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if key != "" {
			if _, exists := os.LookupEnv(key); !exists {
				_ = os.Setenv(key, value)
			}
		}
	}
}

func configurePath() {
	home := envOr("HOME", "/root")
	pathValue := os.Getenv("PATH")
	for _, candidate := range []string{filepath.Join(home, ".local", "bin"), "/root/.local/bin", "/usr/local/bin"} {
		if !pathContains(pathValue, candidate) {
			pathValue = candidate + string(os.PathListSeparator) + pathValue
		}
	}
	_ = os.Setenv("PATH", pathValue)
}

func pathContains(pathValue, candidate string) bool {
	for _, item := range filepath.SplitList(pathValue) {
		if item == candidate {
			return true
		}
	}
	return false
}
