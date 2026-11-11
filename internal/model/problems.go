package model

import (
	"fmt"
	"strings"
)

// Problem is one validation failure. Path points at the field, such as
// "rules[0].select.entities[1]", so the editor can mark it.
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	if p.Path == "" {
		return p.Message
	}
	return p.Path + ": " + p.Message
}

// ValidationError lists every problem Validate found. Use errors.As to get
// it; the admin API returns it as 400 with the list.
type ValidationError struct {
	Problems []Problem `json:"problems"`
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.String()
	}
	return "invalid: " + strings.Join(parts, "; ")
}

// problems collects Problems during one Validate call.
type problems []Problem

func (p *problems) add(path, format string, args ...any) {
	*p = append(*p, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
}

// err returns nil when no problem was found.
func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return &ValidationError{Problems: p}
}

// join builds a field path: join("rules[0]", "select") is
// "rules[0].select", join("", "id") is "id".
func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
