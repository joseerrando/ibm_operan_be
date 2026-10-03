package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Port        string
	DatabaseURL string // DSN go-sql-driver/mysql, contoh: operan:operan@tcp(127.0.0.1:3306)/operan
	JWTSecret   string
	Location    *time.Location
	CORSOrigins []string

	LangflowMock   bool
	LangflowURL    string
	LangflowAPIKey string
	FlowIDs        map[string]string

	InternalToolToken string
	PublicBaseURL     string // dipakai Langflow untuk memanggil internal tools

	STTMock     bool
	STTProvider string // gemini | openai
	STTBaseURL  string
	STTAPIKey   string
	STTModel    string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	tz := env("APP_TIMEZONE", "Asia/Jakarta")
	loc, err := time.LoadLocation(tz)
	if err != nil {
		// Windows tanpa tzdata: Jakarta tetap UTC+7 tanpa DST.
		loc = time.FixedZone("WIB", 7*3600)
	}

	c := &Config{
		Port:        env("PORT", "8080"),
		DatabaseURL: env("DATABASE_URL", "operan:operan@tcp(127.0.0.1:3306)/operan"),
		JWTSecret:   env("JWT_SECRET", "change-me"),
		Location:    loc,
		CORSOrigins: splitList(env("CORS_ORIGINS", "http://localhost:5000")),

		LangflowMock:   envBool("LANGFLOW_MOCK", true),
		LangflowURL:    strings.TrimRight(env("LANGFLOW_URL", "http://localhost:7860"), "/"),
		LangflowAPIKey: env("LANGFLOW_API_KEY", ""),
		FlowIDs: map[string]string{
			"extract_voice_log": env("FLOW_ID_EXTRACT_VOICE", ""),
			"handover_summary":  env("FLOW_ID_HANDOVER", ""),
			"trend_analysis":    env("FLOW_ID_TREND", ""),
			"doctor_report":     env("FLOW_ID_DOCTOR_REPORT", ""),
			"ask_history":       env("FLOW_ID_ASK_HISTORY", ""),
		},

		InternalToolToken: env("INTERNAL_TOOL_TOKEN", "change-me"),
		PublicBaseURL:     strings.TrimRight(env("PUBLIC_BASE_URL", "http://127.0.0.1:8080"), "/"),

		STTMock:     envBool("STT_MOCK", true),
		STTProvider: env("STT_PROVIDER", "gemini"),
		STTBaseURL:  env("STT_BASE_URL", ""),
		STTAPIKey:   env("STT_API_KEY", ""),
		STTModel:    env("STT_MODEL", "gemini-3.1-flash-lite-preview"),
	}
	if c.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET wajib diisi")
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
