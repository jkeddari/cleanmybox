package cleaner

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

type TokenProvider interface {
	TokenBySessionID(sessionID string) (*oauth2.Token, bool)
}

type Config struct {
	OpenAIAPIKey  string
	OpenAIModel   string
	OpenAIBaseURL string
	MaxAIAnalysis int
	DryRun        bool
}

type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusError   Status = "error"
)

type Stats struct {
	TotalScanned       int `json:"total_scanned"`
	Newsletters        int `json:"newsletters"`
	Spam               int `json:"spam"`
	Useless            int `json:"useless"`
	Legit              int `json:"legit"`
	Unsure             int `json:"unsure"`
	AIDeleted          int `json:"ai_deleted"`
	Deleted            int `json:"deleted"`
	UnsubscribedOK     int `json:"unsubscribed_ok"`
	UnsubscribedFailed int `json:"unsubscribed_failed"`
}

type Job struct {
	CheckoutSessionID string    `json:"checkout_session_id"`
	Plan              string    `json:"plan"`
	DryRun            bool      `json:"dry_run"`
	Status            Status    `json:"status"`
	Step              string    `json:"step"`
	ProgressPercent   int       `json:"progress_percent"`
	ProcessedCount    int       `json:"processed_count"`
	TotalCount        int       `json:"total_count"`
	CurrentEmail      string    `json:"current_email,omitempty"`
	LastHeartbeatAt   time.Time `json:"last_heartbeat_at,omitempty"`
	StaleAfterSeconds int       `json:"stale_after_seconds"`
	Error             string    `json:"error,omitempty"`
	StartedAt         time.Time `json:"started_at"`
	FinishedAt        time.Time `json:"finished_at,omitempty"`
	Stats             Stats     `json:"stats"`
}

const (
	jobStaleAfter      = 6 * time.Minute
	jobStallCheckEvery = 15 * time.Second
)

type Service struct {
	tokens        TokenProvider
	ai            *openAIClient
	maxAIAnalysis int
	dryRun        bool

	mu   sync.Mutex
	jobs map[string]Job
}

func NewService(tokens TokenProvider, cfg Config) *Service {
	model := strings.TrimSpace(cfg.OpenAIModel)
	if model == "" {
		model = "gpt-4o-mini"
	}
	maxAI := cfg.MaxAIAnalysis
	if maxAI <= 0 {
		maxAI = 120
	}

	return &Service{
		tokens:        tokens,
		ai:            newOpenAIClient(cfg.OpenAIAPIKey, cfg.OpenAIBaseURL, model),
		maxAIAnalysis: maxAI,
		dryRun:        cfg.DryRun,
		jobs:          make(map[string]Job),
	}
}

func (s *Service) Launch(plan, checkoutSessionID, sessionRef string) {
	job := Job{
		CheckoutSessionID: checkoutSessionID,
		Plan:              plan,
		Status:            StatusPending,
		Step:              "queued",
		ProgressPercent:   1,
		DryRun:            s.dryRun,
		StaleAfterSeconds: int(jobStaleAfter.Seconds()),
	}
	s.saveJob(job)

	go s.run(plan, checkoutSessionID, sessionRef)
}

func (s *Service) Job(checkoutSessionID string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[checkoutSessionID]
	return job, ok
}

func (s *Service) run(plan, checkoutSessionID, sessionRef string) {
	start := time.Now()
	s.updateJob(checkoutSessionID, func(j *Job) {
		j.Status = StatusRunning
		j.Step = "initializing"
		j.ProgressPercent = 3
		j.StartedAt = start
	})

	token, ok := s.tokens.TokenBySessionID(sessionRef)
	if !ok {
		s.failJob(checkoutSessionID, "session expired before cleanup start")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	go s.watchStall(ctx, cancel, checkoutSessionID)
	defer cancel()

	gmailSvc, err := gmail.NewService(ctx, option.WithTokenSource(oauth2.StaticTokenSource(token)))
	if err != nil {
		s.failJob(checkoutSessionID, fmt.Sprintf("gmail init failed: %v", err))
		return
	}

	s.updateJob(checkoutSessionID, func(j *Job) {
		j.Step = "scanning_emails"
		j.ProgressPercent = 5
	})

	messageIDs, err := listInboxMessageIDs(ctx, gmailSvc)
	if err != nil {
		s.failJob(checkoutSessionID, fmt.Sprintf("gmail list failed: %v", err))
		return
	}

	log.Printf("cleanup inbox size fetched (session=%s, total_inbox=%d, dry_run=%t)", checkoutSessionID, len(messageIDs), s.dryRun)
	log.Printf("cleanup scan started (session=%s, plan=%s, dry_run=%t)", checkoutSessionID, plan, s.dryRun)

	stats := Stats{}
	type candidate struct {
		MessageID string
		Subject   string
		From      string
		Snippet   string
		Headers   map[string]string
	}
	candidates := make([]candidate, 0, len(messageIDs))

	s.updateJob(checkoutSessionID, func(j *Job) {
		j.TotalCount = len(messageIDs)
		j.ProcessedCount = 0
		j.Step = "scanning_emails"
		j.ProgressPercent = percentFromCounts(0, len(messageIDs))
	})

	for _, messageID := range messageIDs {
		stats.TotalScanned++

		msg, err := gmailSvc.Users.Messages.Get("me", messageID).
			Format("metadata").
			MetadataHeaders("List-Unsubscribe", "List-Id", "Precedence", "Auto-Submitted", "From", "Subject").
			Context(ctx).
			Do()
		if err != nil {
			s.updateJob(checkoutSessionID, func(j *Job) {
				j.ProcessedCount++
				j.CurrentEmail = ""
				j.Stats = stats
				j.ProgressPercent = percentFromCounts(j.ProcessedCount, j.TotalCount)
			})
			continue
		}

		var partHeaders []*gmail.MessagePartHeader
		if msg.Payload != nil {
			partHeaders = msg.Payload.Headers
		}

		s.updateJob(checkoutSessionID, func(j *Job) {
			j.ProcessedCount++
			j.CurrentEmail = firstNonEmpty(strings.TrimSpace(headersValue(partHeaders, "Subject")), strings.TrimSpace(headersValue(partHeaders, "From")), "(no subject)")
			j.Stats = stats
			j.ProgressPercent = percentFromCounts(j.ProcessedCount, j.TotalCount)
		})

		if stats.TotalScanned%25 == 0 {
			log.Printf("cleanup progress (session=%s, processed=%d/%d)", checkoutSessionID, stats.TotalScanned, len(messageIDs))
		}

		headers := toHeaderMap(partHeaders)
		if !isNewsletter(headers) {
			candidates = append(candidates, candidate{
				MessageID: msg.Id,
				Subject:   headers["subject"],
				From:      headers["from"],
				Snippet:   msg.Snippet,
				Headers:   headers,
			})
			continue
		}

		stats.Newsletters++

		if s.dryRun {
			stats.Deleted++
		} else {
			if _, err := gmailSvc.Users.Messages.Modify("me", msg.Id, &gmail.ModifyMessageRequest{RemoveLabelIds: []string{"UNREAD"}}).Context(ctx).Do(); err != nil {
				log.Printf("gmail modify failed (session=%s, message=%s): %v", checkoutSessionID, msg.Id, err)
			}
			if _, err := gmailSvc.Users.Messages.Trash("me", msg.Id).Context(ctx).Do(); err == nil {
				stats.Deleted++
			} else {
				log.Printf("gmail trash failed (session=%s, message=%s): %v", checkoutSessionID, msg.Id, err)
			}
		}

		if unsubHeader := headers["list-unsubscribe"]; unsubHeader != "" {
			if s.dryRun {
				log.Printf("dry-run unsubscribe skipped (session=%s, message=%s)", checkoutSessionID, msg.Id)
			} else {
				if ok, reason := tryHTTPUnsubscribe(ctx, unsubHeader); ok {
					stats.UnsubscribedOK++
				} else {
					stats.UnsubscribedFailed++
					log.Printf("unsubscribe failed (session=%s, message=%s, reason=%s)", checkoutSessionID, msg.Id, reason)
				}
			}
		}

		s.updateJob(checkoutSessionID, func(j *Job) {
			j.Step = "processing_newsletters"
			j.Stats = stats
		})
	}

	if strings.EqualFold(plan, "cleanplus") {
		s.updateJob(checkoutSessionID, func(j *Job) { j.Step = "processing_ai" })
		if s.ai == nil {
			s.failJob(checkoutSessionID, "cleanplus requested but OPENAI_API_KEY is missing")
			return
		}

		toAnalyze := candidates
		if len(toAnalyze) > s.maxAIAnalysis {
			toAnalyze = toAnalyze[:s.maxAIAnalysis]
		}

		s.updateJob(checkoutSessionID, func(j *Job) {
			j.TotalCount = stats.TotalScanned + len(toAnalyze)
			j.ProgressPercent = percentFromCounts(j.ProcessedCount, j.TotalCount)
		})

		for _, email := range toAnalyze {
			s.updateJob(checkoutSessionID, func(j *Job) {
				j.ProcessedCount++
				j.CurrentEmail = firstNonEmpty(strings.TrimSpace(email.Subject), strings.TrimSpace(email.From), "(no subject)")
				j.ProgressPercent = percentFromCounts(j.ProcessedCount, j.TotalCount)
			})

			decision, err := s.ai.Classify(ctx, aiInput{
				Subject: email.Subject,
				From:    email.From,
				Snippet: email.Snippet,
				Headers: map[string]string{
					"list-unsubscribe": email.Headers["list-unsubscribe"],
					"list-id":          email.Headers["list-id"],
					"precedence":       email.Headers["precedence"],
					"auto-submitted":   email.Headers["auto-submitted"],
				},
			})

			if err != nil {
				stats.Unsure++
				s.updateJob(checkoutSessionID, func(j *Job) { j.Stats = stats })
				continue
			}

			switch decision.Verdict {
			case "spam":
				stats.Spam++
			case "useless":
				stats.Useless++
			case "legit":
				stats.Legit++
			default:
				stats.Unsure++
			}

			if decision.Delete {
				if s.dryRun {
					stats.Deleted++
					stats.AIDeleted++
				} else {
					if _, err := gmailSvc.Users.Messages.Modify("me", email.MessageID, &gmail.ModifyMessageRequest{RemoveLabelIds: []string{"UNREAD"}}).Context(ctx).Do(); err != nil {
						log.Printf("gmail modify failed (session=%s, message=%s): %v", checkoutSessionID, email.MessageID, err)
					}
					if _, err := gmailSvc.Users.Messages.Trash("me", email.MessageID).Context(ctx).Do(); err == nil {
						stats.Deleted++
						stats.AIDeleted++
					} else {
						log.Printf("gmail trash failed (session=%s, message=%s): %v", checkoutSessionID, email.MessageID, err)
					}
				}
			}

			s.updateJob(checkoutSessionID, func(j *Job) { j.Stats = stats })
		}
	}

	s.updateJob(checkoutSessionID, func(j *Job) {
		if j.Status == StatusError {
			return
		}
		j.Status = StatusDone
		j.Step = "completed"
		j.CurrentEmail = ""
		j.ProgressPercent = 100
		j.FinishedAt = time.Now()
		j.Stats = stats
	})

	log.Printf("cleanup finished (plan=%s, session=%s, scanned=%d, newsletters=%d, deleted=%d)", plan, checkoutSessionID, stats.TotalScanned, stats.Newsletters, stats.Deleted)
}

func (s *Service) failJob(checkoutSessionID, errMsg string) {
	s.updateJob(checkoutSessionID, func(j *Job) {
		if j.Status == StatusDone || j.Status == StatusError {
			return
		}
		j.Status = StatusError
		j.Step = "failed"
		j.ProgressPercent = 100
		j.Error = errMsg
		j.FinishedAt = time.Now()
	})
	log.Printf("cleanup failed (session=%s): %s", checkoutSessionID, errMsg)
}

func (s *Service) saveJob(job Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.CheckoutSessionID] = job
}

func (s *Service) updateJob(checkoutSessionID string, fn func(*Job)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[checkoutSessionID]
	if !ok {
		return
	}
	fn(&job)
	if job.Status == StatusRunning {
		job.LastHeartbeatAt = time.Now()
	}
	s.jobs[checkoutSessionID] = job
}

func (s *Service) watchStall(ctx context.Context, cancel context.CancelFunc, checkoutSessionID string) {
	ticker := time.NewTicker(jobStallCheckEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			job, ok := s.Job(checkoutSessionID)
			if !ok {
				return
			}
			if job.Status == StatusDone || job.Status == StatusError {
				return
			}
			if job.LastHeartbeatAt.IsZero() {
				continue
			}
			if time.Since(job.LastHeartbeatAt) > jobStaleAfter {
				log.Printf("cleanup stalled (session=%s, step=%s, last_heartbeat=%s)", checkoutSessionID, job.Step, job.LastHeartbeatAt.Format(time.RFC3339))
				cancel()
				s.failJob(checkoutSessionID, "cleanup stalled: no activity for more than 6 minutes")
				return
			}
		}
	}
}

func listInboxMessageIDs(ctx context.Context, svc *gmail.Service) ([]string, error) {
	ids := make([]string, 0)
	nextPageToken := ""

	for {
		req := svc.Users.Messages.List("me").Q("in:inbox").MaxResults(100).Context(ctx)
		if nextPageToken != "" {
			req = req.PageToken(nextPageToken)
		}
		res, err := req.Do()
		if err != nil {
			return nil, err
		}
		for _, m := range res.Messages {
			ids = append(ids, m.Id)
		}
		if res.NextPageToken == "" {
			break
		}
		nextPageToken = res.NextPageToken
	}

	return ids, nil
}

func toHeaderMap(headers []*gmail.MessagePartHeader) map[string]string {
	m := make(map[string]string, len(headers))
	for _, h := range headers {
		m[strings.ToLower(strings.TrimSpace(h.Name))] = strings.TrimSpace(h.Value)
	}
	return m
}

func headersValue(headers []*gmail.MessagePartHeader, key string) string {
	for _, h := range headers {
		if strings.EqualFold(strings.TrimSpace(h.Name), key) {
			return h.Value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func isNewsletter(headers map[string]string) bool {
	if headers["list-unsubscribe"] != "" {
		return true
	}
	if headers["list-id"] != "" {
		return true
	}
	if precedence := strings.ToLower(headers["precedence"]); precedence == "bulk" || precedence == "list" {
		return true
	}
	if auto := strings.ToLower(headers["auto-submitted"]); auto != "" && auto != "no" {
		return true
	}
	return false
}

func tryHTTPUnsubscribe(ctx context.Context, raw string) (bool, string) {
	seenHTTP := false
	for _, token := range strings.Split(raw, ",") {
		candidate := strings.TrimSpace(strings.Trim(token, "<>"))
		if strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") {
			seenHTTP = true
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate, nil)
			if err != nil {
				log.Printf("unsubscribe request build error (url=%s): %v", candidate, err)
				continue
			}
			resp, err := (&http.Client{Timeout: 7 * time.Second}).Do(req)
			if err != nil {
				log.Printf("unsubscribe request error (url=%s): %v", candidate, err)
				continue
			}
			if resp == nil {
				continue
			}
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				return true, "ok"
			}
			log.Printf("unsubscribe non-success status (url=%s, status=%d)", candidate, resp.StatusCode)
		}
	}
	if !seenHTTP {
		return false, "no_http_unsubscribe_link"
	}
	return false, "all_http_unsubscribe_attempts_failed"
}

func percentFromCounts(processed, total int) int {
	if total <= 0 {
		return 0
	}
	p := (processed * 100) / total
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}
