package handler

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"operan-be/internal/apperr"
	"operan-be/internal/service"
)

// ---------- auth & families ----------

func (h *Handler) register(c *gin.Context) {
	var in struct{ Name, Email, Password string }
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.Register(c, in.Name, in.Email, in.Password)
	respond(c, http.StatusCreated, res, err)
}

func (h *Handler) login(c *gin.Context) {
	var in struct{ Email, Password string }
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.Login(c, in.Email, in.Password)
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) me(c *gin.Context) {
	res, err := h.Svc.Me(c, uid(c))
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) registerDevice(c *gin.Context) {
	var in struct {
		FCMToken string `json:"fcm_token"`
		Platform string `json:"platform"`
	}
	if !bind(c, &in) {
		return
	}
	respond(c, http.StatusNoContent, nil, h.Svc.RegisterDevice(c, uid(c), in.FCMToken, in.Platform))
}

func (h *Handler) createFamily(c *gin.Context) {
	var in struct{ Name string }
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.CreateFamily(c, uid(c), in.Name)
	respond(c, http.StatusCreated, res, err)
}

func (h *Handler) joinFamily(c *gin.Context) {
	var in struct {
		InviteCode string `json:"invite_code"`
	}
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.JoinFamily(c, uid(c), in.InviteCode)
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) getFamily(c *gin.Context) {
	res, err := h.Svc.Family(c, c.Param("id"), uid(c))
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) members(c *gin.Context) {
	res, err := h.Svc.Members(c, c.Param("id"), uid(c))
	respond(c, http.StatusOK, res, err)
}

// ---------- profiles ----------

func (h *Handler) createProfile(c *gin.Context) {
	var in service.ProfileInput
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.CreateProfile(c, c.Param("id"), uid(c), in)
	respond(c, http.StatusCreated, res, err)
}

func (h *Handler) listProfiles(c *gin.Context) {
	res, err := h.Svc.Profiles(c, c.Param("id"), uid(c))
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) getProfile(c *gin.Context) {
	res, err := h.Svc.RequireProfile(c, c.Param("id"), uid(c))
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) updateProfile(c *gin.Context) {
	var in service.ProfileInput
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.UpdateProfile(c, c.Param("id"), uid(c), in)
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) today(c *gin.Context) {
	res, err := h.Svc.Today(c, c.Param("id"), uid(c))
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) timeline(c *gin.Context) {
	day, ok := h.parseDay(c)
	if !ok {
		return
	}
	res, err := h.Svc.Timeline(c, c.Param("id"), uid(c), day)
	respond(c, http.StatusOK, res, err)
}

// ---------- medications & doses ----------

func (h *Handler) createMedication(c *gin.Context) {
	var in service.MedicationInput
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.CreateMedication(c, c.Param("id"), uid(c), in)
	respond(c, http.StatusCreated, res, err)
}

func (h *Handler) listMedications(c *gin.Context) {
	res, err := h.Svc.Medications(c, c.Param("id"), uid(c))
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) updateMedication(c *gin.Context) {
	var in service.MedicationInput
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.UpdateMedication(c, c.Param("id"), uid(c), in)
	respond(c, http.StatusOK, res, err)
}

// listDoses: ?date=YYYY-MM-DD for one day, or ?from=&to= for a range (one query).
func (h *Handler) listDoses(c *gin.Context) {
	if c.Query("from") != "" || c.Query("to") != "" {
		from, to, ok := h.parseRange(c)
		if !ok {
			return
		}
		res, err := h.Svc.DosesRange(c, c.Param("id"), uid(c), from, to)
		respond(c, http.StatusOK, res, err)
		return
	}
	day, ok := h.parseDay(c)
	if !ok {
		return
	}
	res, err := h.Svc.DosesForDate(c, c.Param("id"), uid(c), day)
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) giveDose(c *gin.Context) {
	var in service.GiveInput
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.GiveDose(c, c.Param("id"), uid(c), in)
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) givePRN(c *gin.Context) {
	var in service.GiveInput
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.GivePRN(c, c.Param("id"), uid(c), in)
	respond(c, http.StatusCreated, res, err)
}

func (h *Handler) skipDose(c *gin.Context) {
	var in struct{ Note *string }
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.SkipDose(c, c.Param("id"), uid(c), in.Note)
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) undoDose(c *gin.Context) {
	res, err := h.Svc.UndoDose(c, c.Param("id"), uid(c))
	if err == nil && res == nil {
		c.JSON(http.StatusOK, gin.H{"deleted": true})
		return
	}
	respond(c, http.StatusOK, res, err)
}

// ---------- logs & voice ----------

func (h *Handler) createLog(c *gin.Context) {
	var in service.LogInput
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.CreateLog(c, c.Param("id"), uid(c), in, "manual")
	respond(c, http.StatusCreated, res, err)
}

func (h *Handler) listLogs(c *gin.Context) {
	from, to, ok := h.parseRange(c)
	if !ok {
		return
	}
	res, err := h.Svc.Logs(c, c.Param("id"), uid(c), from, to, c.Query("type"))
	respond(c, http.StatusOK, res, err)
}

// voicePreview accepts multipart "audio" or JSON {"transcript": "..."} (typed fallback).
func (h *Handler) voicePreview(c *gin.Context) {
	var audio []byte
	var mime, transcript string
	if strings.HasPrefix(c.ContentType(), "multipart/") {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.MaxAudioBytes+64<<10)
		fh, err := c.FormFile("audio")
		if err != nil {
			fail(c, apperr.Validation("Rekaman tidak terkirim atau lebih dari 2 MB."))
			return
		}
		if fh.Size > service.MaxAudioBytes {
			fail(c, apperr.Validation("Rekaman terlalu panjang. Maksimal 60 detik."))
			return
		}
		f, err := fh.Open()
		if err != nil {
			fail(c, err)
			return
		}
		defer f.Close()
		if audio, err = io.ReadAll(f); err != nil {
			fail(c, err)
			return
		}
		mime = fh.Header.Get("Content-Type")
	} else {
		var in struct{ Transcript string }
		if !bind(c, &in) {
			return
		}
		if strings.TrimSpace(in.Transcript) == "" {
			fail(c, apperr.Validation("Kirim rekaman suara atau teks catatan."))
			return
		}
		transcript = in.Transcript
	}
	res, err := h.Svc.VoicePreview(c, c.Param("id"), uid(c), audio, mime, transcript)
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) voiceConfirm(c *gin.Context) {
	var in service.VoiceConfirmInput
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.VoiceConfirm(c, c.Param("id"), uid(c), in)
	respond(c, http.StatusCreated, res, err)
}

// ---------- shifts & handovers ----------

func (h *Handler) startShift(c *gin.Context) {
	res, err := h.Svc.StartShift(c, c.Param("id"), uid(c))
	respond(c, http.StatusCreated, res, err)
}

func (h *Handler) handoverDraft(c *gin.Context) {
	res, err := h.Svc.HandoverDraft(c, c.Param("id"), uid(c))
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) endShift(c *gin.Context) {
	var in service.EndShiftInput
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.EndShift(c, c.Param("id"), uid(c), in)
	respond(c, http.StatusCreated, res, err)
}

func (h *Handler) latestHandover(c *gin.Context) {
	res, err := h.Svc.LatestHandover(c, c.Param("id"), uid(c))
	if err == nil && res == nil {
		c.JSON(http.StatusOK, nil)
		return
	}
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) readHandover(c *gin.Context) {
	res, err := h.Svc.MarkHandoverRead(c, c.Param("id"), uid(c))
	respond(c, http.StatusOK, res, err)
}

// ---------- alerts, reports, ask ----------

func (h *Handler) listAlerts(c *gin.Context) {
	res, err := h.Svc.Alerts(c, c.Param("id"), uid(c), c.Query("all") == "1" || c.Query("all") == "true")
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) readAlert(c *gin.Context) {
	res, err := h.Svc.MarkAlertRead(c, c.Param("id"), uid(c))
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) analyze(c *gin.Context) {
	res, err := h.Svc.Analyze(c, c.Param("id"), uid(c))
	respond(c, http.StatusOK, gin.H{"created": res}, err)
}

func (h *Handler) createReport(c *gin.Context) {
	var in service.ReportInput
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.CreateReport(c, c.Param("id"), uid(c), in)
	respond(c, http.StatusCreated, res, err)
}

func (h *Handler) latestReport(c *gin.Context) {
	res, err := h.Svc.LatestReport(c, c.Param("id"), uid(c))
	if err == nil && res == nil {
		c.JSON(http.StatusOK, nil)
		return
	}
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) getReport(c *gin.Context) {
	res, err := h.Svc.Report(c, c.Param("id"), uid(c))
	respond(c, http.StatusOK, res, err)
}

func (h *Handler) ask(c *gin.Context) {
	var in struct{ Question string }
	if !bind(c, &in) {
		return
	}
	res, err := h.Svc.Ask(c, c.Param("id"), uid(c), in.Question)
	respond(c, http.StatusOK, res, err)
}

// ---------- SSE ----------

func (h *Handler) stream(c *gin.Context) {
	fid := c.Param("id")
	if _, err := h.Svc.RequireFamily(c, fid, uid(c)); err != nil {
		fail(c, err)
		return
	}
	ch, unsub := h.Svc.Hub.Subscribe(fid)
	defer unsub()

	w := c.Writer
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: jangan buffer SSE
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "retry: 3000\ndata: {\"type\":\"connected\"}\n\n")
	w.Flush()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			w.Flush()
		case <-heartbeat.C:
			fmt.Fprintf(w, ": ping\n\n")
			w.Flush()
		}
	}
}

// ---------- internal tools (Flow 5 agent) ----------

func (h *Handler) toolLogs(c *gin.Context) {
	pid, ok := h.requireToolSession(c)
	if !ok {
		return
	}
	from, to, ok := h.parseRange(c)
	if !ok {
		return
	}
	res, err := h.Svc.ToolLogs(c, pid, from, to, c.Query("type"))
	respond(c, http.StatusOK, gin.H{"logs": res}, err)
}

func (h *Handler) toolDoses(c *gin.Context) {
	pid, ok := h.requireToolSession(c)
	if !ok {
		return
	}
	from, to, ok := h.parseRange(c)
	if !ok {
		return
	}
	res, err := h.Svc.ToolDoses(c, pid, from, to)
	respond(c, http.StatusOK, gin.H{"doses": res}, err)
}

func (h *Handler) toolMedications(c *gin.Context) {
	pid, ok := h.requireToolSession(c)
	if !ok {
		return
	}
	res, err := h.Svc.ToolMedications(c, pid)
	respond(c, http.StatusOK, gin.H{"medications": res}, err)
}
