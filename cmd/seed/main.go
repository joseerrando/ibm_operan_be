// Command seed fills the database with the demo family (Adik with fever, Kakek with rising blood pressure).
// All dates are relative to "now" so the demo always shows the last 7 days.
//
//	go run ./cmd/seed -reset   (drops every table first)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"golang.org/x/crypto/bcrypt"

	"operan-be/internal/config"
	"operan-be/internal/db"
	"operan-be/internal/langflow"
	"operan-be/internal/service"
	"operan-be/internal/sse"
	"operan-be/internal/stt"
)

type seeder struct {
	ctx   context.Context
	store *db.Store
	svc   *service.Service
	loc   *time.Location
	now   time.Time
}

func main() {
	reset := flag.Bool("reset", false, "hapus semua tabel sebelum mengisi data demo")
	flag.Parse()
	cfg, err := config.Load()
	must(err)
	store, err := db.Open(cfg.DatabaseURL)
	must(err)
	defer store.Close()
	ctx := context.Background()
	if *reset {
		must(store.Reset(ctx))
	}
	must(store.Migrate(ctx))
	if _, err := store.UserByEmail(ctx, "ibu@demo.operan"); err == nil {
		log.Fatal("data demo sudah ada. Jalankan dengan -reset untuk mengisi ulang.")
	}

	mock := langflow.NewMockRunner(cfg.Location)
	mock.Delay = 0
	svc := service.New(store, sse.NewHub(), mock, stt.Mock{}, cfg.Location, cfg.JWTSecret, cfg.PublicBaseURL)
	s := &seeder{ctx: ctx, store: store, svc: svc, loc: cfg.Location, now: time.Now()}
	s.run(envOr("SEED_PASSWORD", "operan123"))
}

func (s *seeder) run(password string) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	must(err)
	user := func(name, email string) *db.User {
		u, err := s.store.CreateUser(s.ctx, name, email, string(hash))
		must(err)
		return u
	}
	ibu := user("Ibu", "ibu@demo.operan")
	ayah := user("Ayah", "ayah@demo.operan")
	nenek := user("Nenek", "nenek@demo.operan")

	fam, err := s.store.CreateFamily(s.ctx, "Keluarga Demo", service.NewInviteCode(), ibu.ID)
	must(err)
	must(s.store.AddMember(s.ctx, fam.ID, ayah.ID, "caregiver"))
	must(s.store.AddMember(s.ctx, fam.ID, nenek.ID, "caregiver"))

	// ---------- Adik (anak): demam 3 hari ----------
	adik := s.profile(fam.ID, "Rara Pratama", "Adik", "anak", s.day(-4*365-40).Format("2006-01-02"),
		"Alergi amoksisilin (menurut keluarga).")
	pct := s.med(adik.ID, "Paracetamol sirup", "obat", "5 ml (1 sendok takar), sesuai resep dokter", nil, true, 240)
	vitD := s.med(adik.ID, "Vitamin D tetes", "vitamin", "1 tetes", []string{"12:00"}, false, 0)

	for d := -6; d <= -1; d++ {
		if d == -3 {
			s.slot(vitD, d, "12:00", nil, 0, 0) // terlewat
			continue
		}
		s.slot(vitD, d, "12:00", []*db.User{ibu, nenek, ayah}[(d+6)%3], 0, 4+d%3)
	}
	s.slot(vitD, 0, "12:00", nil, 0, 0)

	// Riwayat demam: 3 hari berturut-turut >= 38.
	type entry struct {
		day     int
		h, m    int
		celsius float64
		by      *db.User
	}
	temps := []entry{
		{-5, 19, 30, 37.2, ayah}, {-4, 8, 0, 37.4, ibu}, {-3, 7, 50, 37.6, ibu},
		{-2, 8, 10, 38.2, ibu}, {-2, 14, 20, 38.6, nenek}, {-2, 21, 0, 38.9, ayah},
		{-1, 7, 30, 38.4, ibu}, {-1, 13, 40, 38.1, nenek}, {-1, 20, 45, 39.1, ayah},
	}
	for _, t := range temps {
		s.log(adik.ID, t.by, "suhu", map[string]any{"celsius": t.celsius}, s.at(t.day, t.h, t.m))
	}
	for _, d := range []struct {
		day, h, m int
		by        *db.User
	}{{-2, 8, 15, ibu}, {-2, 14, 30, nenek}, {-2, 21, 5, ayah}, {-1, 7, 40, ibu}, {-1, 13, 50, nenek}, {-1, 20, 50, ayah}} {
		s.given(pct, s.at(d.day, d.h, d.m), d.by)
	}
	s.log(adik.ID, ibu, "keluhan", map[string]any{"text": "batuk"}, s.at(-2, 9, 0))
	s.log(adik.ID, nenek, "keluhan", map[string]any{"text": "batuk, rewel setelah tidur siang"}, s.at(-1, 15, 10))
	s.log(adik.ID, ibu, "makan", map[string]any{"portion": "sedikit", "note": ""}, s.at(-2, 12, 30))
	s.log(adik.ID, nenek, "makan", map[string]any{"portion": "setengah", "note": ""}, s.at(-1, 12, 15))
	s.log(adik.ID, ayah, "tidur", map[string]any{"quality": "gelisah", "hours": 6}, s.at(-1, 6, 0))
	s.log(adik.ID, ayah, "tidur", map[string]any{"quality": "gelisah", "hours": 5.5}, s.ago(4*time.Hour))

	// Hari ini: Ayah jaga malam, operan ke Ibu pagi ini. Ibu memberi penurun panas 2,5 jam lalu.
	s.log(adik.ID, ayah, "suhu", map[string]any{"celsius": 38.6}, s.ago(4*time.Hour+20*time.Minute))
	s.given(pct, s.ago(150*time.Minute), ibu)
	s.logViaService(adik, ibu, "suhu", map[string]any{"celsius": 38.4}, s.ago(140*time.Minute))
	s.log(adik.ID, ibu, "keluhan", map[string]any{"text": "batuk"}, s.ago(130*time.Minute))
	s.shiftWithHandover(adik, ayah, ibu, s.ago(14*time.Hour), s.ago(3*time.Hour),
		"Suhu sempat 39,1° pukul 20.45, terakhir 38,6° dini hari. Penurun panas terakhir diberikan pukul 20.50. Tidur gelisah, sempat batuk.",
		[]string{"Vitamin D tetes 12.00"}, []string{"Suhu masih di atas 38°"}, true)

	// ---------- Kakek (lansia): tensi naik 3 hari ----------
	kakek := s.profile(fam.ID, "Hadi Santoso", "Kakek", "lansia", "1952-03-14", "Hipertensi, kontrol rutin di puskesmas.")
	aml := s.med(kakek.ID, "Amlodipin", "obat", "1 tablet 5 mg, sesuai resep dokter", []string{"07:00", "19:00"}, false, 0)
	_ = s.med(kakek.ID, "Vitamin B kompleks", "vitamin", "1 tablet", []string{"08:00"}, false, 0)
	for d := -6; d <= -1; d++ {
		s.slot(aml, d, "07:00", []*db.User{nenek, ayah}[(d+6)%2], 0, 5)
		if d == -1 {
			s.slot(aml, d, "19:00", nil, 0, 0) // terlewat kemarin malam
			continue
		}
		s.slot(aml, d, "19:00", []*db.User{ayah, ibu}[(d+6)%2], 0, 10)
	}
	if s.localNow().Hour() >= 8 {
		s.slot(aml, 0, "07:00", nenek, 0, 7)
	}
	bps := []struct {
		day, h, m, sys, dia int
		by                  *db.User
	}{
		{-6, 7, 20, 142, 88, nenek}, {-5, 7, 15, 145, 90, nenek}, {-4, 7, 30, 148, 90, ayah}, {-3, 7, 10, 150, 92, nenek},
		{-2, 7, 20, 162, 95, nenek}, {-1, 7, 25, 165, 96, ayah},
	}
	for _, b := range bps {
		s.log(kakek.ID, b.by, "tensi", map[string]any{"systolic": b.sys, "diastolic": b.dia}, s.at(b.day, b.h, b.m))
	}
	s.logViaService(kakek, nenek, "tensi", map[string]any{"systolic": 168, "diastolic": 98}, s.ago(70*time.Minute))
	s.log(kakek.ID, nenek, "gula_darah", map[string]any{"mg_dl": 132, "context": "puasa"}, s.ago(75*time.Minute))
	s.log(kakek.ID, nenek, "makan", map[string]any{"portion": "habis", "note": ""}, s.ago(60*time.Minute))
	s.log(kakek.ID, nenek, "keluhan", map[string]any{"text": "pusing ringan saat bangun"}, s.ago(65*time.Minute))
	_, err = s.store.StartShift(s.ctx, kakek.ID, nenek.ID, s.ago(5*time.Hour))
	must(err)

	// Dosis terlewat -> alert "perhatian" seperti yang dibuat worker.
	n, err := s.svc.CheckMissed(s.ctx, time.Now())
	must(err)
	// Slot vitamin 3 hari lalu terlalu lama untuk ditampilkan sebagai peringatan aktif.
	_, _ = s.store.DB.ExecContext(s.ctx, `UPDATE alerts SET is_read = 1 WHERE title LIKE 'Vitamin D tetes jam 12.00 % belum diberikan'
		AND title <> 'Vitamin D tetes jam 12.00 belum diberikan' AND created_at >= ?`, time.Now().Add(-time.Minute).UTC())

	fmt.Println("Data demo siap.")
	fmt.Println("  Keluarga     :", fam.Name, " kode undangan", fam.InviteCode)
	fmt.Println("  Akun         : ibu@demo.operan, ayah@demo.operan, nenek@demo.operan")
	fmt.Println("  Kata sandi   :", password)
	fmt.Println("  Dosis terlewat ditandai:", n)
}

// ---------- helpers ----------

func (s *seeder) localNow() time.Time { return s.now.In(s.loc) }

func (s *seeder) day(offset int) time.Time {
	y, m, d := s.localNow().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, s.loc).AddDate(0, 0, offset)
}

func (s *seeder) at(dayOffset, h, m int) time.Time {
	return s.day(dayOffset).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

func (s *seeder) ago(d time.Duration) time.Time { return s.now.Add(-d) }

func (s *seeder) profile(familyID, name, nick, typ, birth, notes string) *db.Profile {
	p := &db.Profile{FamilyID: familyID, Name: name, Nickname: &nick, ProfileType: typ, BirthDate: &birth, Notes: &notes,
		TrackedMetrics: service.DefaultMetrics(typ)}
	if typ == "anak" {
		p.TrackedMetrics = []string{"suhu", "makan", "tidur", "keluhan"}
	}
	must(s.store.CreateProfile(s.ctx, p))
	_, err := s.store.DB.ExecContext(s.ctx, `UPDATE care_profiles SET created_at = ? WHERE id = ?`, s.day(-8).UTC(), p.ID)
	must(err)
	return p
}

func (s *seeder) med(profileID, name, kind, label string, times []string, prn bool, interval int) *db.Medication {
	m := &db.Medication{CareProfileID: profileID, Name: name, Kind: kind, DoseLabel: label, ScheduleTimes: times, IsPRN: prn}
	if interval > 0 {
		m.MinIntervalMinutes = &interval
	}
	must(s.store.CreateMedication(s.ctx, m))
	m.CreatedAt = s.day(-8)
	_, err := s.store.DB.ExecContext(s.ctx, `UPDATE medications SET created_at = ? WHERE id = ?`, m.CreatedAt.UTC(), m.ID)
	must(err)
	return m
}

// slot creates a scheduled dose; when by != nil it is given `lateMin` minutes after the slot.
func (s *seeder) slot(m *db.Medication, dayOffset int, clock string, by *db.User, _ int, lateMin int) {
	h, mi, err := service.ParseClock(clock)
	must(err)
	at := s.at(dayOffset, h, mi)
	must(s.store.InsertPendingDose(s.ctx, m.ID, at))
	if by == nil {
		return
	}
	var id string
	must(s.store.DB.QueryRowContext(s.ctx, `SELECT id FROM medication_doses WHERE medication_id = ? AND scheduled_at = ?`, m.ID, at.UTC()).Scan(&id))
	must(s.store.MarkDoseGiven(s.ctx, id, at.Add(time.Duration(lateMin)*time.Minute), by.ID, nil, false))
	s.backdateMark(id, at.Add(time.Duration(lateMin)*time.Minute))
}

func (s *seeder) given(m *db.Medication, at time.Time, by *db.User) {
	id, err := s.store.InsertGivenDose(s.ctx, m.ID, at, by.ID, nil, false)
	must(err)
	s.backdateMark(id, at)
}

// backdateMark keeps the "Batalkan" window closed for historical doses.
func (s *seeder) backdateMark(doseID string, at time.Time) {
	_, err := s.store.DB.ExecContext(s.ctx, `UPDATE medication_doses SET marked_at = ? WHERE id = ?`, at.UTC(), doseID)
	must(err)
}

func (s *seeder) log(profileID string, by *db.User, typ string, value map[string]any, at time.Time) {
	v, _ := json.Marshal(value)
	val, err := service.ValidateLogValue(typ, v)
	must(err)
	uid := by.ID
	must(s.store.CreateLog(s.ctx, &db.CareLog{CareProfileID: profileID, AuthorID: &uid, LogType: typ, Value: val, Source: "manual", RecordedAt: at}))
}

// logViaService runs the deterministic alert rules exactly like the app does.
func (s *seeder) logViaService(p *db.Profile, by *db.User, typ string, value map[string]any, at time.Time) {
	v, _ := json.Marshal(value)
	ts := at.UTC().Format(time.RFC3339)
	_, err := s.svc.CreateLog(s.ctx, p.ID, by.ID, service.LogInput{LogType: typ, Value: v, RecordedAt: &ts}, "manual")
	must(err)
}

func (s *seeder) shiftWithHandover(p *db.Profile, from, to *db.User, start, end time.Time, summary string, pending, watch []string, read bool) {
	sh, err := s.store.StartShift(s.ctx, p.ID, from.ID, start)
	must(err)
	must(s.store.EndShift(s.ctx, sh.ID, end))
	h := &db.Handover{CareProfileID: p.ID, FromShiftID: &sh.ID, FromUserID: &from.ID, ToUserID: &to.ID, Summary: summary,
		PendingItems: pending, WatchItems: watch, AIGenerated: true}
	must(s.store.CreateHandover(s.ctx, h))
	_, err = s.store.DB.ExecContext(s.ctx, `UPDATE handovers SET created_at = ?, read_at = ? WHERE id = ?`, end.UTC(), nullIf(read, end.Add(10*time.Minute).UTC()), h.ID)
	must(err)
	_, err = s.store.StartShift(s.ctx, p.ID, to.ID, end)
	must(err)
}

func nullIf(ok bool, t time.Time) any {
	if !ok {
		return nil
	}
	return t
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
