package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/jkeddari/cleanmybox/internal/auth"
	"github.com/jkeddari/cleanmybox/internal/cleaner"
	"github.com/jkeddari/cleanmybox/internal/ui/pages"
)

type Handlers struct {
	authSvc    *auth.Service
	cleanerSvc *cleaner.Service
}

func New(authSvc *auth.Service, cleanerSvc *cleaner.Service) *Handlers {
	return &Handlers{authSvc: authSvc, cleanerSvc: cleanerSvc}
}

func (h *Handlers) HomePage(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, pages.Home(h.authSvc.IsLoggedIn(r)))
}

func (h *Handlers) LoginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, pages.Login())
}

func (h *Handlers) CleanupPage(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	canceled := r.URL.Query().Get("canceled") == "1"
	h.render(w, r, pages.Cleanup(sessionID, canceled))
}

func (h *Handlers) JobFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "HX-Request")

	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		h.render(w, r, pages.JobStatus(pages.JobStatusView{SessionID: "", Found: false}))
		return
	}

	job, found := h.cleanerSvc.Job(sessionID)
	if !found {
		h.render(w, r, pages.JobStatus(pages.JobStatusView{SessionID: sessionID, Found: false}))
		return
	}

	view := pages.JobStatusView{
		SessionID:       sessionID,
		Found:           true,
		Plan:            strings.ToUpper(job.Plan),
		Status:          string(job.Status),
		Step:            job.Step,
		ProgressPercent: job.ProgressPercent,
		ProcessedCount:  job.ProcessedCount,
		TotalCount:      job.TotalCount,
		CurrentEmail:    firstNonEmpty(job.CurrentEmail, "waiting for next email"),
		DryRun:          job.DryRun,
		StaleAfterSec:   job.StaleAfterSeconds,
		Error:           job.Error,
		Stats: pages.JobStatsView{
			Deleted:            job.Stats.Deleted,
			Newsletters:        job.Stats.Newsletters,
			Spam:               job.Stats.Spam,
			Useless:            job.Stats.Useless,
			Legit:              job.Stats.Legit,
			Unsure:             job.Stats.Unsure,
			AIDeleted:          job.Stats.AIDeleted,
			TotalScanned:       job.Stats.TotalScanned,
			UnsubscribedOK:     job.Stats.UnsubscribedOK,
			UnsubscribedFailed: job.Stats.UnsubscribedFailed,
		},
	}

	if !job.LastHeartbeatAt.IsZero() {
		view.HeartbeatAgeSec = int(time.Since(job.LastHeartbeatAt).Seconds())
		view.Stale = view.HeartbeatAgeSec > maxInt(0, job.StaleAfterSeconds)
	}

	h.render(w, r, pages.JobStatus(view))
}

func (h *Handlers) render(w http.ResponseWriter, r *http.Request, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = component.Render(r.Context(), w)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
