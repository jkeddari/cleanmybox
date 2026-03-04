package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/jkeddari/cleanmybox/internal/ui/pages"
)

func (s *Server) HomePage(w http.ResponseWriter, r *http.Request) {
	loggedIn := s.authSvc.IsLoggedIn(r)
	email, _ := s.authSvc.EmailFromRequest(r)
	s.render(w, r, pages.Home(loggedIn, email))
}

func (s *Server) LoginPage(w http.ResponseWriter, r *http.Request) {
	loggedIn := s.authSvc.IsLoggedIn(r)
	email, _ := s.authSvc.EmailFromRequest(r)
	s.render(w, r, pages.Login(loggedIn, email))
}

func (s *Server) CleanupPage(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	canceled := r.URL.Query().Get("canceled") == "1"
	loggedIn := s.authSvc.IsLoggedIn(r)
	email, _ := s.authSvc.EmailFromRequest(r)
	s.render(w, r, pages.Cleanup(loggedIn, email, sessionID, canceled))
}

func (s *Server) JobFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "HX-Request")

	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		s.render(w, r, pages.JobStatus(pages.JobStatusView{SessionID: "", Found: false}))
		return
	}

	job, found := s.cleaner.Job(sessionID)
	if !found {
		s.render(w, r, pages.JobStatus(pages.JobStatusView{SessionID: sessionID, Found: false}))
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
		Error:           job.Error,
		Stats: pages.JobStatsView{
			Deleted:            job.Stats.Deleted,
			Kept:               job.Stats.Kept,
			AIScanned:          job.Stats.AIScanned,
			ScanFailed:         job.Stats.ScanFailed,
			Newsletters:        job.Stats.Newsletters,
			Spam:               job.Stats.Spam,
			Useless:            job.Stats.Useless,
			Legit:              job.Stats.Legit,
			Unsure:             job.Stats.Unsure,
			Archived:           job.Stats.Archived,
			Unsubscribed:       job.Stats.Unsubscribed,
			TotalScanned:       job.Stats.TotalScanned,
			UnsubscribedFailed: job.Stats.UnsubscribedFailed,
		},
	}
	view.ScanDuration = formatDuration(job.StartedAt, job.FinishedAt)

	s.render(w, r, pages.JobStatus(view))
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, component templ.Component) {
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

func formatDuration(startedAt, finishedAt time.Time) string {
	if startedAt.IsZero() {
		return "0s"
	}
	end := finishedAt
	if end.IsZero() {
		end = time.Now()
	}
	if end.Before(startedAt) {
		return "0s"
	}
	d := end.Sub(startedAt).Round(time.Second)
	if d < time.Minute {
		return d.String()
	}
	mins := int(d / time.Minute)
	secs := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm %02ds", mins, secs)
}
