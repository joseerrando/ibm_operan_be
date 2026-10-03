// Package stt turns a short voice recording into text before it goes to Flow 1.
package stt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

var ErrEmpty = errors.New("stt: rekaman tidak terdengar jelas")

// Hint gives the transcriber context (used by the mock and as a prompt for Gemini).
type Hint struct {
	ProfileName string
	ProfileType string
	MedNames    []string
}

type Transcriber interface {
	Transcribe(ctx context.Context, audio []byte, mime string, hint Hint) (string, error)
}

// ---------- mock ----------

type Mock struct{}

func (Mock) Transcribe(ctx context.Context, audio []byte, mime string, h Hint) (string, error) {
	name := h.ProfileName
	if name == "" {
		name = "Adik"
	}
	switch h.ProfileType {
	case "lansia", "kronis":
		return fmt.Sprintf("Tensi %s 158 per 92, obat tensi sudah diminum jam 7, makannya setengah", name), nil
	case "pemulihan":
		return fmt.Sprintf("%s suhunya 37,6, makan habis, masih agak pusing", name), nil
	}
	return fmt.Sprintf("%s suhunya 38,5, sudah minum obat penurun panas jam 9", name), nil
}

// ---------- Gemini (Google AI Studio) ----------

type Gemini struct {
	BaseURL string // default https://generativelanguage.googleapis.com
	APIKey  string
	Model   string
	Client  *http.Client
}

func NewGemini(baseURL, apiKey, model string) *Gemini {
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	return &Gemini{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model, Client: &http.Client{Timeout: 45 * time.Second}}
}

func (g *Gemini) Transcribe(ctx context.Context, audio []byte, mime string, h Hint) (string, error) {
	if g.APIKey == "" {
		return "", errors.New("stt: STT_API_KEY belum diisi")
	}
	prompt := "Transkripsikan rekaman suara berbahasa Indonesia ini kata per kata. " +
		"Tulis angka sebagai digit dengan koma desimal (contoh 38,5). " +
		"Balas HANYA teks transkrip, tanpa penjelasan. Jika tidak ada ucapan yang jelas, balas kosong."
	if h.ProfileName != "" {
		prompt += " Nama orang yang dirawat: " + h.ProfileName + "."
	}
	if len(h.MedNames) > 0 {
		prompt += " Nama obat yang mungkin disebut: " + strings.Join(h.MedNames, ", ") + "."
	}
	body, _ := json.Marshal(map[string]any{
		"contents": []any{map[string]any{"parts": []any{
			map[string]any{"text": prompt},
			map[string]any{"inline_data": map[string]string{"mime_type": cleanMime(mime), "data": base64.StdEncoding.EncodeToString(audio)}},
		}}},
		"generationConfig": map[string]any{"temperature": 0},
	})
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", g.BaseURL, g.Model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.APIKey)
	resp, err := g.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("stt: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("stt: gemini status %d: %.300s", resp.StatusCode, raw)
	}
	var r struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("stt: respons gemini tidak terbaca: %w", err)
	}
	var sb strings.Builder
	for _, c := range r.Candidates {
		for _, p := range c.Content.Parts {
			sb.WriteString(p.Text)
		}
		break
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return "", ErrEmpty
	}
	return text, nil
}

// cleanMime drops codec parameters ("audio/webm;codecs=opus" -> "audio/webm").
func cleanMime(m string) string {
	if i := strings.Index(m, ";"); i > 0 {
		m = m[:i]
	}
	m = strings.TrimSpace(m)
	switch m {
	case "", "application/octet-stream":
		return "audio/webm"
	case "audio/x-m4a", "audio/m4a":
		return "audio/mp4"
	}
	return m
}

// ---------- OpenAI-compatible Whisper ----------

type OpenAI struct {
	BaseURL, APIKey, Model string
	Client                 *http.Client
}

func NewOpenAI(baseURL, apiKey, model string) *OpenAI {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAI{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model, Client: &http.Client{Timeout: 45 * time.Second}}
}

func (o *OpenAI) Transcribe(ctx context.Context, audio []byte, mime string, h Hint) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	ext := "webm"
	if strings.Contains(mime, "mp4") || strings.Contains(mime, "m4a") {
		ext = "m4a"
	}
	fw, _ := w.CreateFormFile("file", "audio."+ext)
	_, _ = fw.Write(audio)
	_ = w.WriteField("model", o.Model)
	_ = w.WriteField("language", "id")
	_ = w.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/audio/transcriptions", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+o.APIKey)
	resp, err := o.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("stt: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("stt: status %d: %.300s", resp.StatusCode, raw)
	}
	var r struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", err
	}
	if strings.TrimSpace(r.Text) == "" {
		return "", ErrEmpty
	}
	return strings.TrimSpace(r.Text), nil
}
