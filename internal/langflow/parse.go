package langflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrNoText = errors.New("langflow: respons tidak berisi teks output")

// ExtractText pulls the output message text out of a /api/v1/run response.
// The documented path is outputs[0].outputs[0].results.message.text, but the
// structure differs between Langflow versions, so a few fallbacks are tried.
func ExtractText(body []byte) (string, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return "", fmt.Errorf("langflow: respons bukan JSON: %w", err)
	}
	first := dig(root, "outputs", 0, "outputs", 0)
	candidates := []any{
		dig(first, "results", "message", "text"),
		dig(first, "results", "message", "data", "text"),
		dig(first, "results", "text", "text"),
		dig(first, "outputs", "message", "message"),
		dig(first, "outputs", "message", "text"),
		dig(first, "messages", 0, "message"),
		dig(first, "artifacts", "message"),
	}
	for _, c := range candidates {
		if s, ok := c.(string); ok && strings.TrimSpace(s) != "" {
			return s, nil
		}
	}
	if s := findText(first, 0); s != "" {
		return s, nil
	}
	return "", ErrNoText
}

func dig(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

// findText does a bounded depth-first search for a "text" string field.
func findText(v any, depth int) string {
	if depth > 8 || v == nil {
		return ""
	}
	switch t := v.(type) {
	case map[string]any:
		if s, ok := t["text"].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
		for _, k := range []string{"results", "message", "outputs", "data", "text"} {
			if s := findText(t[k], depth+1); s != "" {
				return s
			}
		}
	case []any:
		for _, e := range t {
			if s := findText(e, depth+1); s != "" {
				return s
			}
		}
	}
	return ""
}

// ExtractJSON strips ```json fences and surrounding prose and returns the JSON object.
func ExtractJSON(text string) (string, error) {
	s := strings.TrimSpace(text)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		rest = strings.TrimPrefix(rest, "json")
		rest = strings.TrimPrefix(rest, "JSON")
		if j := strings.Index(rest, "```"); j >= 0 {
			rest = rest[:j]
		}
		s = strings.TrimSpace(rest)
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return "", fmt.Errorf("langflow: output tidak berisi objek JSON")
	}
	s = s[start : end+1]
	if !json.Valid([]byte(s)) {
		return "", fmt.Errorf("langflow: output JSON tidak valid")
	}
	return s, nil
}

// DecodeOutput turns flow text into out (a pointer) and validates it when possible.
func DecodeOutput(text string, out any) error {
	if sp, ok := out.(*string); ok {
		*sp = text
		return nil
	}
	js, err := ExtractJSON(text)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(js), out); err != nil {
		return fmt.Errorf("langflow: output tidak sesuai skema: %w", err)
	}
	if v, ok := out.(validator); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("langflow: output tidak sesuai skema: %w", err)
		}
	}
	return nil
}
