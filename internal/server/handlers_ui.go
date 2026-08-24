package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/jkeddari/cleanmybox/internal/history"
	"github.com/jkeddari/cleanmybox/internal/ui/pages"
)

func (s *Server) HomePage(w http.ResponseWriter, r *http.Request) {
	loggedIn := s.authSvc.IsLoggedIn(r)
	email, _ := s.authSvc.EmailFromRequest(r)
	s.render(w, r, pages.Home(loggedIn, email))
}

func (s *Server) GmailInboxCleanerPage(w http.ResponseWriter, r *http.Request) {
	loggedIn := s.authSvc.IsLoggedIn(r)
	email, _ := s.authSvc.EmailFromRequest(r)
	s.render(w, r, pages.GmailInboxCleaner(loggedIn, email))
}

func (s *Server) OutlookInboxCleanerPage(w http.ResponseWriter, r *http.Request) {
	loggedIn := s.authSvc.IsLoggedIn(r)
	email, _ := s.authSvc.EmailFromRequest(r)
	s.render(w, r, pages.OutlookInboxCleaner(loggedIn, email))
}

func (s *Server) GuidesPage(w http.ResponseWriter, r *http.Request) {
	loggedIn := s.authSvc.IsLoggedIn(r)
	email, _ := s.authSvc.EmailFromRequest(r)
	s.render(w, r, pages.Guides(loggedIn, email))
}

func (s *Server) LoginPage(w http.ResponseWriter, r *http.Request) {
	loggedIn := s.authSvc.IsLoggedIn(r)
	email, _ := s.authSvc.EmailFromRequest(r)
	s.render(w, r, pages.Login(loggedIn, email))
}

func (s *Server) PrivacyPage(w http.ResponseWriter, r *http.Request) {
	loggedIn := s.authSvc.IsLoggedIn(r)
	email, _ := s.authSvc.EmailFromRequest(r)
	s.render(w, r, pages.LegalPrivacy(loggedIn, email))
}

func (s *Server) TermsPage(w http.ResponseWriter, r *http.Request) {
	loggedIn := s.authSvc.IsLoggedIn(r)
	email, _ := s.authSvc.EmailFromRequest(r)
	s.render(w, r, pages.LegalTerms(loggedIn, email))
}

func (s *Server) CleanupPage(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	canceled := r.URL.Query().Get("canceled") == "1"
	loggedIn := s.authSvc.IsLoggedIn(r)
	email, _ := s.authSvc.EmailFromRequest(r)
	s.render(w, r, pages.Cleanup(loggedIn, email, sessionID, canceled))
}

func (s *Server) HistoryPage(w http.ResponseWriter, r *http.Request) {
	if !s.authSvc.IsLoggedIn(r) {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	email, ok := s.authSvc.EmailFromRequest(r)
	if !ok || strings.TrimSpace(email) == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	runs := make([]history.RunRecord, 0)
	if s.history != nil {
		historyRuns, err := s.history.ListRunsByEmail(r.Context(), email, 20)
		if err != nil {
			http.Error(w, "failed to load history", http.StatusInternalServerError)
			return
		}
		runs = historyRuns
	}

	view := toHistoryView(email, runs)
	s.render(w, r, pages.History(true, email, view))
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

func toHistoryView(email string, runs []history.RunRecord) pages.HistoryView {
	viewRuns := make([]pages.HistoryRunView, 0, len(runs))
	totalDeleted := 0
	totalArchived := 0
	successRuns := 0
	failedRuns := 0

	for _, run := range runs {
		status := strings.ToLower(strings.TrimSpace(run.Status))
		if status == "done" {
			successRuns++
		} else if status == "error" {
			failedRuns++
		}

		totalDeleted += run.Stats.Deleted
		totalArchived += run.Stats.Archived

		createdAt := "-"
		if run.CreatedAtUnix > 0 {
			createdAt = time.Unix(run.CreatedAtUnix, 0).Local().Format("2006-01-02 15:04")
		}

		plan := strings.ToUpper(strings.TrimSpace(run.Plan))
		if plan == "" {
			plan = "CLEAN"
		}

		viewRuns = append(viewRuns, pages.HistoryRunView{
			CreatedAt:       createdAt,
			Plan:            plan,
			Status:          status,
			StatusLabel:     statusLabel(status),
			Duration:        formatSeconds(run.DurationSeconds),
			DryRun:          run.DryRun,
			Error:           strings.TrimSpace(run.Error),
			CheckoutSession: run.CheckoutSessionID,
			Stats: pages.JobStatsView{
				Deleted:            run.Stats.Deleted,
				Kept:               run.Stats.Kept,
				AIScanned:          run.Stats.AIScanned,
				ScanFailed:         run.Stats.ScanFailed,
				Newsletters:        run.Stats.Newsletters,
				Spam:               run.Stats.Spam,
				Useless:            run.Stats.Useless,
				Legit:              run.Stats.Legit,
				Unsure:             run.Stats.Unsure,
				Archived:           run.Stats.Archived,
				Unsubscribed:       run.Stats.Unsubscribed,
				TotalScanned:       run.Stats.TotalScanned,
				UnsubscribedFailed: run.Stats.UnsubscribedFailed,
			},
		})
	}

	successRate := 0
	if len(runs) > 0 {
		successRate = (successRuns * 100) / len(runs)
	}

	return pages.HistoryView{
		Email:         email,
		Runs:          viewRuns,
		TotalRuns:     len(runs),
		SuccessRuns:   successRuns,
		FailedRuns:    failedRuns,
		SuccessRate:   successRate,
		DeletedTotal:  totalDeleted,
		ArchivedTotal: totalArchived,
	}
}

func statusLabel(status string) string {
	if status == "done" {
		return "Completed"
	}
	if status == "error" {
		return "Failed"
	}
	if status == "running" {
		return "Running"
	}
	return "Unknown"
}

func formatSeconds(seconds int) string {
	if seconds <= 0 {
		return "0s"
	}
	d := time.Duration(seconds) * time.Second
	if d < time.Minute {
		return d.String()
	}
	mins := int(d / time.Minute)
	secs := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm %02ds", mins, secs)
}
