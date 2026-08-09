package observability

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

type Logger struct {
	mu        sync.Mutex
	output    io.Writer
	component string
	now       func() time.Time
}

func NewLogger(output io.Writer, component string) *Logger {
	return &Logger{output: output, component: component, now: time.Now}
}

func (l *Logger) Info(message string, fields map[string]any)  { l.write("info", message, fields) }
func (l *Logger) Warn(message string, fields map[string]any)  { l.write("warn", message, fields) }
func (l *Logger) Error(message string, fields map[string]any) { l.write("error", message, fields) }

func (l *Logger) write(level, message string, fields map[string]any) {
	if l == nil || l.output == nil {
		return
	}
	record := make(map[string]any, len(fields)+4)
	record["time"] = l.now().UTC().Format(time.RFC3339Nano)
	record["level"] = level
	record["component"] = l.component
	record["message"] = message
	for key, value := range fields {
		if key != "time" && key != "level" && key != "component" && key != "message" {
			record[key] = value
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = json.NewEncoder(l.output).Encode(record)
}
