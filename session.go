package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errAgySessionBusy  = errors.New("the requested conversation is already processing a turn")
	errAgySessionLimit = errors.New("the maximum number of active conversations has been reached")
)

type agySessionManager struct {
	app         *application
	mu          sync.Mutex
	sessions    map[string]*agySession
	maxSessions int
	idleTimeout time.Duration
	nextPending uint64
}

type agySession struct {
	mu         sync.Mutex
	manager    *agySessionManager
	key        string
	id         string
	opts       requestOptions
	lastUsed   time.Time
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	output     chan sessionOutput
	done       chan struct{}
	stop       chan struct{}
	stopOnce   sync.Once
	started    bool
	finished   atomic.Bool
	processErr error
}

type sessionOutput struct {
	line string
	err  error
}

func newAgySessionManager(app *application, maxSessions int, idleTimeout time.Duration) *agySessionManager {
	return &agySessionManager{
		app:         app,
		sessions:    make(map[string]*agySession),
		maxSessions: maxSessions,
		idleTimeout: idleTimeout,
	}
}

// acquire returns a session with its mutex held. A conversation can process only one turn at a time.
func (manager *agySessionManager) acquire(conversationID string, opts requestOptions) (*agySession, error) {
	manager.mu.Lock()
	if conversationID != "" {
		if session, ok := manager.sessions[conversationID]; ok {
			if !session.mu.TryLock() {
				manager.mu.Unlock()
				return nil, errAgySessionBusy
			}
			if !session.finished.Load() && sessionOptionsMatch(session.opts, opts) {
				manager.mu.Unlock()
				return session, nil
			}
			delete(manager.sessions, conversationID)
			session.stopLocked()
			session.mu.Unlock()
		}
	}

	if len(manager.sessions) >= manager.maxSessions && !manager.evictOldestIdleLocked() {
		manager.mu.Unlock()
		return nil, errAgySessionLimit
	}

	manager.nextPending++
	key := conversationID
	if key == "" {
		key = fmt.Sprintf("pending-%d", manager.nextPending)
	}
	opts.ConversationID = conversationID
	session := &agySession{
		manager: manager,
		key:     key,
		id:      conversationID,
		opts:    opts,
	}
	session.mu.Lock()
	manager.sessions[key] = session
	manager.mu.Unlock()
	return session, nil
}

func sessionOptionsMatch(left, right requestOptions) bool {
	return left.Model == right.Model && left.Effort == right.Effort && left.WorkspaceDir == right.WorkspaceDir
}

func (manager *agySessionManager) evictOldestIdleLocked() bool {
	var oldestKey string
	var oldest *agySession
	for key, session := range manager.sessions {
		if !session.mu.TryLock() {
			continue
		}
		if oldest == nil || session.lastUsed.Before(oldest.lastUsed) {
			if oldest != nil {
				oldest.mu.Unlock()
			}
			oldestKey = key
			oldest = session
		} else {
			session.mu.Unlock()
		}
	}
	if oldest == nil {
		return false
	}
	delete(manager.sessions, oldestKey)
	oldest.stopLocked()
	oldest.mu.Unlock()
	return true
}

func (manager *agySessionManager) bind(session *agySession, conversationID string) error {
	if conversationID == "" {
		return nil
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if existing, ok := manager.sessions[conversationID]; ok && existing != session {
		if !existing.mu.TryLock() {
			return errAgySessionBusy
		}
		delete(manager.sessions, conversationID)
		existing.stopLocked()
		existing.mu.Unlock()
	}
	if session.key != conversationID {
		delete(manager.sessions, session.key)
		session.key = conversationID
		manager.sessions[conversationID] = session
	}
	session.id = conversationID
	return nil
}

func (manager *agySessionManager) release(session *agySession) {
	manager.mu.Lock()
	if manager.sessions[session.key] == session {
		session.lastUsed = time.Now()
	}
	manager.mu.Unlock()
	session.mu.Unlock()
}

func (manager *agySessionManager) discard(session *agySession) {
	manager.mu.Lock()
	if manager.sessions[session.key] == session {
		delete(manager.sessions, session.key)
	}
	manager.mu.Unlock()
	session.stopLocked()
}

func (manager *agySessionManager) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			manager.closeIdleSessions()
		}
	}
}

func (manager *agySessionManager) closeIdleSessions() {
	manager.mu.Lock()
	now := time.Now()
	for key, session := range manager.sessions {
		if now.Sub(session.lastUsed) < manager.idleTimeout || !session.mu.TryLock() {
			continue
		}
		delete(manager.sessions, key)
		session.stopLocked()
		session.mu.Unlock()
	}
	manager.mu.Unlock()
}

func (manager *agySessionManager) closeAll() {
	manager.mu.Lock()
	sessions := make([]*agySession, 0, len(manager.sessions))
	for key, session := range manager.sessions {
		delete(manager.sessions, key)
		sessions = append(sessions, session)
	}
	manager.mu.Unlock()

	for _, session := range sessions {
		session.mu.Lock()
		session.stopLocked()
		session.mu.Unlock()
	}
}

func (app *application) runAgyTurn(ctx context.Context, prompt string, opts requestOptions, onEvent func(map[string]any) error) (commandResult, error) {
	if app.sessions == nil {
		return commandResult{}, errors.New("Antigravity session manager is not initialized")
	}
	session, err := app.sessions.acquire(opts.ConversationID, opts)
	if err != nil {
		return commandResult{}, err
	}
	result, err := session.runTurn(ctx, prompt, onEvent)
	if err != nil {
		app.sessions.discard(session)
	}
	if result.Meta == nil {
		result.Meta = make(map[string]any)
	}
	if session.id != "" {
		result.Meta["conversation_id"] = session.id
	}
	app.sessions.release(session)
	return result, err
}

func (session *agySession) runTurn(ctx context.Context, prompt string, onEvent func(map[string]any) error) (commandResult, error) {
	if !session.started {
		if err := session.start(); err != nil {
			return commandResult{}, err
		}
	}
	message, err := json.Marshal(map[string]any{
		"event":   "user",
		"message": map[string]any{"content": prompt},
	})
	if err != nil {
		return commandResult{}, fmt.Errorf("encode Antigravity prompt: %w", err)
	}
	message = append(message, '\n')
	if _, err := session.stdin.Write(message); err != nil {
		return commandResult{}, fmt.Errorf("send prompt to Antigravity session: %w", err)
	}

	tools := make([]any, 0)
	seenTools := make(map[string]struct{})
	for {
		select {
		case <-ctx.Done():
			return commandResult{}, ctx.Err()
		case output, ok := <-session.output:
			if !ok {
				<-session.done
				if session.processErr != nil {
					return commandResult{}, fmt.Errorf("Antigravity session exited: %w", session.processErr)
				}
				return commandResult{}, errors.New("Antigravity session closed before returning a result")
			}
			if output.err != nil {
				return commandResult{}, fmt.Errorf("read Antigravity output: %w", output.err)
			}
			var event map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(output.line)), &event); err != nil {
				continue
			}
			switch stringValue(event["event"]) {
			case "init":
				if id := stringValue(event["conversation_id"]); id != "" {
					if err := session.manager.bind(session, id); err != nil {
						return commandResult{}, err
					}
				}
			case "step_update":
				step, _ := event["step_update"].(map[string]any)
				if step != nil && stringValue(step["step_type"]) == "tool" {
					toolInfo, _ := step["tool_info"].(map[string]any)
					name := firstNonEmpty(stringValue(step["tool_name"]), nestedString(toolInfo, "name"), "tool")
					key := fmt.Sprintf("%v:%s", step["step_index"], name)
					if _, found := seenTools[key]; !found {
						seenTools[key] = struct{}{}
						var args any
						if toolInfo != nil {
							args = toolInfo["parameters"]
						}
						tools = append(tools, map[string]any{"name": name, "args": args})
					}
				}
			case "result":
				payload, _ := event["result"].(map[string]any)
				if payload == nil {
					return commandResult{}, errors.New("Antigravity returned an empty result event")
				}
				if id := stringValue(payload["conversation_id"]); id != "" {
					if err := session.manager.bind(session, id); err != nil {
						return commandResult{}, err
					}
				}
				result := commandResult{
					Response: strings.TrimSpace(stringValue(payload["response"])),
					Tools:    tools,
					Meta:     payload,
				}
				status := stringValue(payload["status"])
				if status != "" && status != "SUCCESS" {
					return result, fmt.Errorf("Antigravity returned status %s: %s", status, stringValue(payload["error"]))
				}
				return result, nil
			}
			if onEvent != nil {
				if err := onEvent(event); err != nil {
					return commandResult{}, err
				}
			}
		}
	}
}

func (session *agySession) start() error {
	args := []string{
		"--dangerously-skip-permissions",
		"--add-dir", session.opts.WorkspaceDir,
		"--input-format", "stream-json",
		"--output-format", "stream-json",
	}
	if session.id != "" {
		args = append(args, "--conversation", session.id)
	}
	if session.opts.Model != "" {
		args = append(args, "--model", session.opts.Model)
	}
	if session.opts.Effort != "" {
		args = append(args, "--effort", session.opts.Effort)
	}

	cmd := exec.Command(session.manager.app.agyPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open Antigravity output: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("open Antigravity diagnostics: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open Antigravity input: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Antigravity CLI: %w", err)
	}
	session.cmd = cmd
	session.stdin = stdin
	session.output = make(chan sessionOutput, 128)
	session.done = make(chan struct{})
	session.stop = make(chan struct{})
	session.started = true
	go session.readOutput(stdout)
	go logCommandStderr(stderr)
	return nil
}

func (session *agySession) readOutput(reader io.Reader) {
	defer close(session.output)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxCommandOutput)
	stopping := false
	for scanner.Scan() {
		if stopping {
			continue
		}
		select {
		case session.output <- sessionOutput{line: scanner.Text()}:
		case <-session.stop:
			stopping = true
		}
	}
	if err := scanner.Err(); err != nil && !stopping {
		select {
		case session.output <- sessionOutput{err: err}:
		case <-session.stop:
		}
	}
	session.processErr = session.cmd.Wait()
	session.finished.Store(true)
	close(session.done)
}

func (session *agySession) stopLocked() {
	if !session.started || session.cmd == nil {
		return
	}
	session.stopOnce.Do(func() { close(session.stop) })
	if session.stdin != nil {
		_ = session.stdin.Close()
	}
	select {
	case <-session.done:
	case <-time.After(2 * time.Second):
		if session.cmd.Process != nil {
			_ = session.cmd.Process.Kill()
		}
		<-session.done
	}
}

func (app *application) fallbackIfAgyMissing(ctx context.Context, query string, opts requestOptions, nativeErr error) (commandResult, error) {
	if !errors.Is(nativeErr, exec.ErrNotFound) {
		return commandResult{}, nativeErr
	}
	log.Printf("Antigravity CLI was not found; using the configured companion fallback")
	return app.runCompanionFallback(ctx, query, opts)
}
