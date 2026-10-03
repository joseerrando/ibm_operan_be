package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"operan-be/internal/db"
	"operan-be/internal/langflow"
	"operan-be/internal/middleware"
	"operan-be/internal/service"
	"operan-be/internal/sse"
	"operan-be/internal/stt"
)

// Integration tests run against a real MariaDB/MySQL database.
// Default: operan_test on local XAMPP. Override with TEST_DATABASE_URL.
func setup(t *testing.T) (*gin.Engine, *service.Service) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "operan:operan@tcp(127.0.0.1:3306)/operan_test"
	}
	store, err := db.Open(dsn)
	if err != nil {
		t.Skipf("database test tidak tersedia: %v", err)
	}
	ctx := context.Background()
	if err := store.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	loc := time.FixedZone("WIB", 7*3600)
	mock := langflow.NewMockRunner(loc)
	mock.Delay = 0
	svc := service.New(store, sse.NewHub(), mock, stt.Mock{}, loc, "test-secret", "http://localhost:8080")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.Recovery())
	(&Handler{Svc: svc, InternalToken: "tool-secret", Limiter: middleware.NewRateLimiter()}).Register(r)
	return r, svc
}

type client struct {
	t     *testing.T
	r     *gin.Engine
	token string
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	w := httptest.NewRecorder()
	c.r.ServeHTTP(w, req)
	var out map[string]any
	if w.Body.Len() > 0 && strings.HasPrefix(strings.TrimSpace(w.Body.String()), "{") {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

func (c *client) list(path string) []any {
	c.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	w := httptest.NewRecorder()
	c.r.ServeHTTP(w, req)
	if w.Code != 200 {
		c.t.Fatalf("GET %s = %d %s", path, w.Code, w.Body.String())
	}
	var out []any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

func signup(t *testing.T, r *gin.Engine, name, email string) *client {
	t.Helper()
	c := &client{t: t, r: r}
	code, res := c.do("POST", "/api/v1/auth/register", map[string]string{"name": name, "email": email, "password": "rahasia123"})
	if code != 201 {
		t.Fatalf("register %s: %d %v", email, code, res)
	}
	code, res = c.do("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": "rahasia123"})
	if code != 200 {
		t.Fatalf("login: %d %v", code, res)
	}
	c.token = res["token"].(string)
	return c
}

func errCode(res map[string]any) string {
	if e, ok := res["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func TestFamilyFlowAndAuthorization(t *testing.T) {
	r, _ := setup(t)
	ibu := signup(t, r, "Ibu", "ibu@test.id")
	nenek := signup(t, r, "Nenek", "nenek@test.id")
	asing := signup(t, r, "Orang lain", "asing@test.id")

	code, fam := ibu.do("POST", "/api/v1/families", map[string]string{"name": "Keluarga Uji"})
	if code != 201 {
		t.Fatalf("create family %d %v", code, fam)
	}
	fid := fam["id"].(string)
	inv := fam["invite_code"].(string)
	if len(inv) != 7 || inv[3] != '-' || strings.ContainsAny(inv, "O0I1") {
		t.Fatalf("invite code format: %q", inv)
	}

	code, res := nenek.do("POST", "/api/v1/families/join", map[string]string{"invite_code": strings.ToLower(strings.ReplaceAll(inv, "-", ""))})
	if code != 200 || res["role"] != "caregiver" {
		t.Fatalf("join %d %v", code, res)
	}

	code, prof := ibu.do("POST", "/api/v1/families/"+fid+"/profiles", map[string]any{"name": "Adik Rara", "nickname": "Adik", "profile_type": "anak"})
	if code != 201 {
		t.Fatalf("create profile %d %v", code, prof)
	}
	pid := prof["id"].(string)
	metrics := prof["tracked_metrics"].([]any)
	if len(metrics) != 4 || metrics[0] != "suhu" {
		t.Errorf("default metrics for anak: %v", metrics)
	}

	if ps := nenek.list("/api/v1/families/" + fid + "/profiles"); len(ps) != 1 {
		t.Fatalf("second member should see the profile, got %v", ps)
	}
	if code, res := asing.do("GET", "/api/v1/profiles/"+pid, nil); code != 403 || errCode(res) != "FORBIDDEN" {
		t.Fatalf("non-member should get 403, got %d %v", code, res)
	}
	if code, _ := asing.do("GET", "/api/v1/profiles/"+pid+"/today", nil); code != 403 {
		t.Fatalf("non-member today should be 403, got %d", code)
	}
	if code, _ := (&client{t: t, r: r}).do("GET", "/api/v1/auth/me", nil); code != 401 {
		t.Fatalf("no token should be 401, got %d", code)
	}
}

func TestDoubleDoseFlow(t *testing.T) {
	r, _ := setup(t)
	ibu := signup(t, r, "Ibu", "ibu@test.id")
	nenek := signup(t, r, "Nenek", "nenek@test.id")
	_, fam := ibu.do("POST", "/api/v1/families", map[string]string{"name": "K"})
	fid := fam["id"].(string)
	nenek.do("POST", "/api/v1/families/join", map[string]string{"invite_code": fam["invite_code"].(string)})
	_, prof := ibu.do("POST", "/api/v1/families/"+fid+"/profiles", map[string]any{"name": "Adik", "profile_type": "anak"})
	pid := prof["id"].(string)

	code, med := ibu.do("POST", "/api/v1/profiles/"+pid+"/medications", map[string]any{
		"name": "Paracetamol sirup", "kind": "obat", "dose_label": "1 sendok takar 5 ml", "is_prn": true, "min_interval_minutes": 240})
	if code != 201 {
		t.Fatalf("create med %d %v", code, med)
	}
	mid := med["id"].(string)

	code, dose := ibu.do("POST", "/api/v1/medications/"+mid+"/give-prn", map[string]any{})
	if code != 201 || dose["status"] != "given" {
		t.Fatalf("first give %d %v", code, dose)
	}

	code, res := nenek.do("POST", "/api/v1/medications/"+mid+"/give-prn", map[string]any{})
	if code != 409 || errCode(res) != "DOUBLE_DOSE" {
		t.Fatalf("second give should be 409 DOUBLE_DOSE, got %d %v", code, res)
	}
	details := res["error"].(map[string]any)["details"].(map[string]any)
	if details["last_given_by_name"] != "Ibu" || details["allowed_from"] == nil {
		t.Fatalf("details should name Ibu and allowed_from: %v", details)
	}

	code, forced := nenek.do("POST", "/api/v1/medications/"+mid+"/give-prn", map[string]any{"force": true, "reason": "Dokter jaga menyarankan"})
	if code != 201 || forced["forced"] != true {
		t.Fatalf("forced give %d %v", code, forced)
	}

	// Undo hanya oleh pencatat.
	if code, _ := ibu.do("POST", "/api/v1/doses/"+forced["id"].(string)+"/undo", nil); code != 403 {
		t.Fatalf("undo by other user should be 403, got %d", code)
	}
	if code, _ := nenek.do("POST", "/api/v1/doses/"+forced["id"].(string)+"/undo", nil); code != 200 {
		t.Fatalf("undo by recorder should succeed, got %d", code)
	}

	// Jadwal tetap: slot yang sama tidak boleh dua kali.
	now := time.Now().In(time.FixedZone("WIB", 7*3600))
	slot := now.Add(-10 * time.Minute).Format("15:04")
	_, vit := ibu.do("POST", "/api/v1/profiles/"+pid+"/medications", map[string]any{
		"name": "Vitamin D", "kind": "vitamin", "dose_label": "1 tetes", "schedule_times": []string{slot}})
	_ = vit
	doses := ibu.list("/api/v1/profiles/" + pid + "/doses")
	var vitDose string
	for _, d := range doses {
		dm := d.(map[string]any)
		if dm["medication_name"] == "Vitamin D" {
			vitDose = dm["id"].(string)
		}
	}
	if vitDose == "" {
		if now.Hour() == 0 && now.Minute() < 10 {
			t.Skip("slot jatuh di hari sebelumnya")
		}
		t.Fatalf("scheduled vitamin dose not generated: %v", doses)
	}
	if code, _ := ibu.do("POST", "/api/v1/doses/"+vitDose+"/give", map[string]any{}); code != 200 {
		t.Fatalf("give scheduled %d", code)
	}
	if code, res := nenek.do("POST", "/api/v1/doses/"+vitDose+"/give", map[string]any{}); code != 409 || errCode(res) != "DOUBLE_DOSE" {
		t.Fatalf("same slot twice should be 409, got %d %v", code, res)
	}

	// Riwayat: satu request untuk rentang tanggal.
	from := time.Now().AddDate(0, 0, -7).UTC().Format(time.RFC3339)
	to := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	ranged := ibu.list("/api/v1/profiles/" + pid + "/doses?from=" + from + "&to=" + to)
	given := 0
	for _, d := range ranged {
		if d.(map[string]any)["status"] == "given" {
			given++
		}
	}
	if given < 2 { // PRN pertama + vitamin
		t.Fatalf("range should include given doses, got %d of %v", given, ranged)
	}
}

func TestLogsValidationTodayAndAI(t *testing.T) {
	r, svc := setup(t)
	ibu := signup(t, r, "Ibu", "ibu@test.id")
	nenek := signup(t, r, "Nenek", "nenek@test.id")
	_, fam := ibu.do("POST", "/api/v1/families", map[string]string{"name": "K"})
	fid := fam["id"].(string)
	_, join := nenek.do("POST", "/api/v1/families/join", map[string]string{"invite_code": fam["invite_code"].(string)})
	_ = join
	_, prof := ibu.do("POST", "/api/v1/families/"+fid+"/profiles", map[string]any{"name": "Adik", "profile_type": "anak"})
	pid := prof["id"].(string)
	ibu.do("POST", "/api/v1/profiles/"+pid+"/medications", map[string]any{
		"name": "Paracetamol sirup", "kind": "obat", "dose_label": "5 ml", "is_prn": true, "min_interval_minutes": 240})

	if code, res := ibu.do("POST", "/api/v1/profiles/"+pid+"/logs", map[string]any{"log_type": "suhu", "value": map[string]any{"celsius": 45}}); code != 422 {
		t.Fatalf("out-of-range temperature should be 422, got %d %v", code, res)
	}
	if code, res := ibu.do("POST", "/api/v1/profiles/"+pid+"/logs", map[string]any{"log_type": "tensi", "value": map[string]any{"systolic": 120, "diastolic": 130}}); code != 422 {
		t.Fatalf("diastolic >= systolic should be 422, got %d %v", code, res)
	}
	if code, res := ibu.do("POST", "/api/v1/profiles/"+pid+"/logs", map[string]any{"log_type": "suhu", "value": map[string]any{"celsius": 39.6}}); code != 201 {
		t.Fatalf("valid log %d %v", code, res)
	}

	code, today := ibu.do("GET", "/api/v1/profiles/"+pid+"/today", nil)
	if code != 200 {
		t.Fatalf("today %d %v", code, today)
	}
	if today["primary_metric"] != "suhu" || today["latest"].(map[string]any)["suhu"] == nil {
		t.Fatalf("today latest suhu missing: %v", today["latest"])
	}
	if alerts := today["alerts"].([]any); len(alerts) == 0 {
		t.Fatalf("39.6 should raise a penting alert")
	}

	// Suara (teks) -> preview -> konfirmasi.
	code, prev := ibu.do("POST", "/api/v1/profiles/"+pid+"/voice", map[string]any{"transcript": "Adik suhunya 38,5, sudah minum obat penurun panas"})
	if code != 200 {
		t.Fatalf("voice preview %d %v", code, prev)
	}
	logs := prev["logs"].([]any)
	doses := prev["doses"].([]any)
	if len(logs) != 1 || len(doses) != 1 {
		t.Fatalf("expected 1 log + 1 dose, got %v", prev)
	}
	d0 := doses[0].(map[string]any)
	code, conf := ibu.do("POST", "/api/v1/profiles/"+pid+"/voice/confirm", map[string]any{
		"logs":  []any{logs[0]},
		"doses": []any{map[string]any{"medication_id": d0["medication_id"], "given_at": d0["given_at"]}},
	})
	if code != 201 {
		t.Fatalf("voice confirm %d %v", code, conf)
	}
	// Konfirmasi kedua untuk dosis yang sama tertahan aturan dosis ganda.
	if code, res := nenek.do("POST", "/api/v1/profiles/"+pid+"/voice/confirm", map[string]any{
		"doses": []any{map[string]any{"medication_id": d0["medication_id"], "given_at": time.Now().UTC().Format(time.RFC3339)}},
	}); code != 409 || errCode(res) != "DOUBLE_DOSE" {
		t.Fatalf("voice dose within interval should be 409, got %d %v", code, res)
	}

	// Giliran jaga -> draft -> serahkan -> dibaca.
	_, sh := ibu.do("POST", "/api/v1/profiles/"+pid+"/shifts/start", nil)
	sid := sh["id"].(string)
	code, draft := ibu.do("GET", "/api/v1/shifts/"+sid+"/handover-draft", nil)
	if code != 200 || draft["summary"] == "" {
		t.Fatalf("draft %d %v", code, draft)
	}
	_, me := nenek.do("GET", "/api/v1/auth/me", nil)
	nenekID := me["user"].(map[string]any)["id"].(string)
	code, ho := ibu.do("POST", "/api/v1/shifts/"+sid+"/end", map[string]any{"to_user_id": nenekID, "summary_override": draft["summary"]})
	if code != 201 {
		t.Fatalf("end shift %d %v", code, ho)
	}
	_, nt := nenek.do("GET", "/api/v1/profiles/"+pid+"/today", nil)
	if nt["handover"] == nil || nt["on_duty"].(map[string]any)["name"] != "Nenek" {
		t.Fatalf("receiver should see handover and be on duty: %v / %v", nt["handover"], nt["on_duty"])
	}
	if code, _ := nenek.do("PATCH", "/api/v1/handovers/"+ho["id"].(string)+"/read", nil); code != 200 {
		t.Fatalf("read handover %d", code)
	}
	_, it := ibu.do("GET", "/api/v1/profiles/"+pid+"/today", nil)
	if sent := it["sent_handover"].(map[string]any); sent["read_at"] == nil {
		t.Fatalf("sender should see read_at: %v", sent)
	}

	// Laporan & Tanya.
	code, rep := ibu.do("POST", "/api/v1/profiles/"+pid+"/reports", map[string]any{})
	if code != 201 || rep["series"].(map[string]any)["suhu"] == nil {
		t.Fatalf("report %d %v", code, rep)
	}
	code, ans := ibu.do("POST", "/api/v1/profiles/"+pid+"/ask", map[string]any{"question": "Jam berapa terakhir Adik minum obat penurun panas?"})
	if code != 200 || len(ans["sources"].([]any)) != 1 || !strings.Contains(ans["answer"].(string), "Paracetamol") {
		t.Fatalf("ask %d %v", code, ans)
	}

	// Internal tools: butuh token statis + sesi untuk profil ini.
	req := httptest.NewRequest("GET", "/internal/tools/profiles/"+pid+"/logs?session=salah", nil)
	req.Header.Set("X-Internal-Token", "tool-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("tool without valid session should be 403, got %d", w.Code)
	}
	_ = svc
}
