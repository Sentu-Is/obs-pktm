package logging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Logger struct {
	mu   sync.Mutex
	file *os.File
}

type entry struct {
	Time    string         `json:"time"`
	Level   string         `json:"level"`
	Action  string         `json:"action"`
	Message string         `json:"message,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
}

func NewLastSession(dir string) (*Logger, error) {
	if dir == "" {
		dir = "logs"
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	path := filepath.Join(dir, "last_session.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}

	logger := &Logger{file: file}
	logger.Info("session_start", "obs-pktm started", map[string]any{
		"log_path": path,
	})
	return logger, nil
}

func (l *Logger) Close() {
	if l == nil || l.file == nil {
		return
	}

	l.Info("session_end", "obs-pktm stopped", nil)

	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.file.Close()
	l.file = nil
}

func (l *Logger) Info(action, message string, fields map[string]any) {
	l.write("info", action, message, fields)
}

func (l *Logger) Error(action, message string, fields map[string]any) {
	l.write("error", action, message, fields)
}

func (l *Logger) write(level, action, message string, fields map[string]any) {
	if l == nil || l.file == nil {
		return
	}

	event := entry{
		Time:    time.Now().Format(time.RFC3339Nano),
		Level:   level,
		Action:  action,
		Message: message,
		Fields:  fields,
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_, _ = l.file.Write(data)
	}
}
