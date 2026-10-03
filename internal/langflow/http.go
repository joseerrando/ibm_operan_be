package langflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type HTTPRunner struct {
	BaseURL string
	APIKey  string
	FlowIDs map[string]string
	Client  *http.Client
	Timeout time.Duration
}

func NewHTTPRunner(baseURL, apiKey string, flowIDs map[string]string) *HTTPRunner {
	return &HTTPRunner{BaseURL: baseURL, APIKey: apiKey, FlowIDs: flowIDs,
		Client: &http.Client{}, Timeout: 30 * time.Second}
}

func (r *HTTPRunner) Mock() bool { return false }

// Run calls POST /api/v1/run/{flow_id} with the payload as a JSON string, retrying once.
func (r *HTTPRunner) Run(ctx context.Context, flow FlowName, payload any, out any) error {
	id := r.FlowIDs[string(flow)]
	if id == "" {
		return fmt.Errorf("langflow: FLOW_ID untuk %s belum diisi", flow)
	}
	input, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// Semua flow Operan memakai Chat Input -> ... -> Chat Output, jadi payload dikirim sebagai pesan chat.
	body, _ := json.Marshal(map[string]string{
		"input_value": string(input), "input_type": "chat", "output_type": "chat",
	})

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		text, err := r.call(ctx, id, body)
		if err == nil {
			if err = DecodeOutput(text, out); err == nil {
				return nil
			}
			slog.Warn("langflow output tidak valid", "flow", flow, "err", err)
		}
		lastErr = err
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			break
		}
	}
	return lastErr
}

func (r *HTTPRunner) call(ctx context.Context, flowID string, body []byte) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.BaseURL+"/api/v1/run/"+flowID+"?stream=false", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.APIKey != "" {
		req.Header.Set("x-api-key", r.APIKey)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("langflow: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("langflow: status %d: %s", resp.StatusCode, truncate(raw, 300))
	}
	text, err := ExtractText(raw)
	if err != nil {
		// Struktur respons berbeda: catat bentuknya (tanpa isi kesehatan di level info).
		slog.Debug("langflow raw response", "body", truncate(raw, 2000))
		return "", err
	}
	return text, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
