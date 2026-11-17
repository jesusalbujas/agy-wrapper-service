package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type installedPlugin struct {
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Version string   `json:"version,omitempty"`
	Source  string   `json:"source,omitempty"`
	Skills  []string `json:"skills,omitempty"`
	Agents  []string `json:"agents,omitempty"`
}

func (app *application) handleAgents(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	output, err := app.runCatalogCommand("agents")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "Could not list Antigravity agents", "details": err.Error()})
		return
	}
	agents := make([]string, 0)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			agents = append(agents, line)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (app *application) handlePlugins(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	plugins, err := app.listInstalledPlugins()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "Could not list Antigravity plugins", "details": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugins": plugins})
}

func (app *application) handleSkills(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	set := make(map[string]string)
	if plugins, err := app.listInstalledPlugins(); err == nil {
		for _, plugin := range plugins {
			if !plugin.Enabled {
				continue
			}
			for _, skill := range plugin.Skills {
				set[skill] = "plugin:" + plugin.Name
			}
		}
	}
	for _, root := range app.skillRoots() {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || entry.Name() != "SKILL.md" {
				return nil
			}
			name := filepath.Base(filepath.Dir(path))
			if name != "skills" && name != "" {
				if _, exists := set[name]; !exists {
					set[name] = "local"
				}
			}
			return nil
		})
	}
	type skillInfo struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	skills := make([]skillInfo, 0, len(set))
	for name, source := range set {
		skills = append(skills, skillInfo{Name: name, Source: source})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"skills": skills})
}

func (app *application) listInstalledPlugins() ([]installedPlugin, error) {
	output, err := app.runCatalogCommand("plugin", "list", "--json")
	if err != nil {
		return nil, err
	}
	var plugins []installedPlugin
	if err := json.Unmarshal(output, &plugins); err != nil {
		return nil, errors.New("Antigravity returned an invalid plugin list")
	}
	return plugins, nil
}

func (app *application) runCatalogCommand(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, app.agyPath, args...).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return nil, errors.New(message)
	}
	return output, nil
}

func (app *application) skillRoots() []string {
	return []string{
		filepath.Join(app.geminiDir, "config", "skills"),
		filepath.Join(app.geminiDir, "antigravity-cli", "skills"),
		filepath.Join(app.geminiDir, "antigravity-cli", "builtin", "skills"),
	}
}
