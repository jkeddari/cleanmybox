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
	AIScanned          int `json:"ai_scanned"`
	ScanFailed         int `json:"scan_failed"`
	Newsletters        int `json:"newsletters"`
	Spam               int `json:"spam"`
	Useless            int `json:"useless"`
	Legit              int `json:"legit"`
	Unsure             int `json:"unsure"`
	Archived           int `json:"archived"`
	Kept               int `json:"kept"`
	Deleted            int `json:"deleted"`
	Unsubscribed       int `json:"unsubscribed"`
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

type pipelineResult struct {
	currentEmail string
	stats        Stats
}

const (
	jobStaleAfter           = 6 * time.Minute
	jobStallCheckEvery      = 15 * time.Second
	defaultPipelineWorkers  = 6
	defaultAIRequestsPerSec = 4
	defaultAIMaxRetries     = 5
	defaultAIInitialBackoff = 500 * time.Millisecond
	defaultAIMaxBackoff     = 8 * time.Second
)

type Service struct {
	tokens TokenProvider
	ai     *openAIClient
	dryRun bool

	mu   sync.Mutex
	jobs map[string]Job
}

func NewService(tokens TokenProvider, cfg Config) *Service {
	model := strings.TrimSpace(cfg.OpenAIModel)
	if model == "" {
		model = "gpt-4o-mini"
	}

	return &Service{
		tokens: tokens,
		ai:     newOpenAIClient(cfg.OpenAIAPIKey, cfg.OpenAIBaseURL, model),
		dryRun: cfg.DryRun,
		jobs:   make(map[string]Job),
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
	cleanPlus := strings.EqualFold(plan, "cleanplus")
	var scanner *aiScan
	if cleanPlus {
		if s.ai == nil {
			s.failJob(checkoutSessionID, "cleanplus requested but OPENAI_API_KEY is missing")
			return
		}
		scanner = newAIScan(s, defaultAIRequestsPerSec)
		defer scanner.Close()
	}

	s.updateJob(checkoutSessionID, func(j *Job) {
		j.TotalCount = len(messageIDs)
		j.ProcessedCount = 0
		j.Step = "processing_emails"
		j.ProgressPercent = percentFromCounts(0, len(messageIDs))
	})

	if len(messageIDs) > 0 {
		workerCount := defaultPipelineWorkers
		if workerCount > len(messageIDs) {
			workerCount = len(messageIDs)
		}
		if workerCount < 1 {
			workerCount = 1
		}

		jobsCh := make(chan string)
		resultsCh := make(chan pipelineResult, workerCount)

		var wg sync.WaitGroup
		for i := 0; i < workerCount; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for messageID := range jobsCh {
					result := s.processMessage(ctx, gmailSvc, checkoutSessionID, messageID, cleanPlus, scanner)
					select {
					case <-ctx.Done():
						return
					case resultsCh <- result:
					}
				}
			}()
		}

		go func() {
			defer close(jobsCh)
			for _, messageID := range messageIDs {
				select {
				case <-ctx.Done():
					return
				case jobsCh <- messageID:
				}
			}
		}()

		go func() {
			wg.Wait()
			close(resultsCh)
		}()

		processed := 0
		for result := range resultsCh {
			processed++
			mergeStats(&stats, result.stats)

			if processed%25 == 0 {
				log.Printf("cleanup progress (session=%s, processed=%d/%d)", checkoutSessionID, processed, len(messageIDs))
			}

			s.updateJob(checkoutSessionID, func(j *Job) {
				j.ProcessedCount = processed
				j.CurrentEmail = firstNonEmpty(result.currentEmail, "")
				j.Stats = stats
				j.ProgressPercent = percentFromCounts(j.ProcessedCount, j.TotalCount)
				j.Step = "processing_emails"
			})
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

type aiScan struct {
	tokens <-chan struct{}
	stop   func()
	svc    *Service
}

func newAIScan(svc *Service, requestsPerSec int) *aiScan {
	tokens, stop := newAIRateLimiter(requestsPerSec)
	return &aiScan{
		tokens: tokens,
		stop:   stop,
		svc:    svc,
	}
}

func (a *aiScan) Close() {
	if a == nil || a.stop == nil {
		return
	}
	a.stop()
	a.stop = nil
}

func (a *aiScan) Classify(ctx context.Context, input aiInput) (aiDecision, error) {
	if err := waitForAIRateToken(ctx, a.tokens); err != nil {
		return aiDecision{}, err
	}
	return a.svc.classifyWithRetry(ctx, input)
}

func (s *Service) processMessage(ctx context.Context, gmailSvc *gmail.Service, checkoutSessionID, messageID string, cleanPlus bool, scanner *aiScan) pipelineResult {
	result := pipelineResult{}
	result.stats.TotalScanned = 1

	msg, err := gmailSvc.Users.Messages.Get("me", messageID).
		Format("metadata").
		MetadataHeaders("List-Unsubscribe", "List-Id", "Precedence", "Auto-Submitted", "From", "Subject").
		Context(ctx).
		Do()
	if err != nil {
		result.stats.ScanFailed = 1
		return result
	}

	var partHeaders []*gmail.MessagePartHeader
	if msg.Payload != nil {
		partHeaders = msg.Payload.Headers
	}

	result.currentEmail = firstNonEmpty(strings.TrimSpace(headersValue(partHeaders, "Subject")), strings.TrimSpace(headersValue(partHeaders, "From")), "(no subject)")

	headers := toHeaderMap(partHeaders)
	if isNewsletter(headers) {
		result.stats.Newsletters = 1

		if s.dryRun {
			result.stats.Deleted = 1
		} else {
			if _, err := gmailSvc.Users.Messages.Modify("me", msg.Id, &gmail.ModifyMessageRequest{RemoveLabelIds: []string{"UNREAD"}}).Context(ctx).Do(); err != nil {
				log.Printf("gmail modify failed (session=%s, message=%s): %v", checkoutSessionID, msg.Id, err)
			}
			if _, err := gmailSvc.Users.Messages.Trash("me", msg.Id).Context(ctx).Do(); err == nil {
				result.stats.Deleted = 1
			} else {
				log.Printf("gmail trash failed (session=%s, message=%s): %v", checkoutSessionID, msg.Id, err)
			}
		}

		if unsubHeader := headers["list-unsubscribe"]; unsubHeader != "" {
			if s.dryRun {
				log.Printf("dry-run unsubscribe skipped (session=%s, message=%s)", checkoutSessionID, msg.Id)
			} else {
				result.stats.Unsubscribed = 1
				if ok, reason := tryHTTPUnsubscribe(ctx, unsubHeader); !ok {
					result.stats.UnsubscribedFailed = 1
					log.Printf("unsubscribe failed (session=%s, message=%s, reason=%s)", checkoutSessionID, msg.Id, reason)
				}
			}
		}

		return result
	}

	if !cleanPlus {
		return result
	}

	result.stats.AIScanned = 1
	decision, err := scanner.Classify(ctx, aiInput{
		Subject: headers["subject"],
		From:    headers["from"],
		Snippet: msg.Snippet,
		Headers: map[string]string{
			"list-unsubscribe": headers["list-unsubscribe"],
			"list-id":          headers["list-id"],
			"precedence":       headers["precedence"],
			"auto-submitted":   headers["auto-submitted"],
		},
	})
	if err != nil {
		result.stats.Unsure = 1
		result.stats.Kept = 1
		return result
	}

	switch decision.Verdict {
	case "useless":
		result.stats.Useless = 1
	case "legit":
		result.stats.Legit = 1
	case "spam":
		// Spam action count is tracked when action is applied.
	default:
		result.stats.Unsure = 1
	}

	actionStats := Stats{}
	applyAIDecisionAction(ctx, gmailSvc, checkoutSessionID, msg.Id, decision.Action, s.dryRun, &actionStats)
	result.stats.Deleted += actionStats.Deleted
	result.stats.Archived += actionStats.Archived
	result.stats.Spam += actionStats.Spam
	result.stats.Kept += actionStats.Kept

	return result
}

func mergeStats(target *Stats, delta Stats) {
	target.TotalScanned += delta.TotalScanned
	target.AIScanned += delta.AIScanned
	target.ScanFailed += delta.ScanFailed
	target.Newsletters += delta.Newsletters
	target.Spam += delta.Spam
	target.Useless += delta.Useless
	target.Legit += delta.Legit
	target.Unsure += delta.Unsure
	target.Archived += delta.Archived
	target.Kept += delta.Kept
	target.Deleted += delta.Deleted
	target.Unsubscribed += delta.Unsubscribed
	target.UnsubscribedFailed += delta.UnsubscribedFailed
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

func applyAIDecisionAction(ctx context.Context, gmailSvc *gmail.Service, checkoutSessionID, messageID, action string, dryRun bool, stats *Stats) {
	switch action {
	case "delete":
		if dryRun {
			stats.Deleted++
			return
		}
		if _, err := gmailSvc.Users.Messages.Modify("me", messageID, &gmail.ModifyMessageRequest{RemoveLabelIds: []string{"UNREAD"}}).Context(ctx).Do(); err != nil {
			log.Printf("gmail modify failed (session=%s, message=%s): %v", checkoutSessionID, messageID, err)
		}
		if _, err := gmailSvc.Users.Messages.Trash("me", messageID).Context(ctx).Do(); err != nil {
			log.Printf("gmail trash failed (session=%s, message=%s): %v", checkoutSessionID, messageID, err)
			return
		}
		stats.Deleted++
	case "archive":
		if dryRun {
			stats.Archived++
			return
		}
		if _, err := gmailSvc.Users.Messages.Modify("me", messageID, &gmail.ModifyMessageRequest{RemoveLabelIds: []string{"UNREAD", "INBOX"}}).Context(ctx).Do(); err != nil {
			log.Printf("gmail archive failed (session=%s, message=%s): %v", checkoutSessionID, messageID, err)
			return
		}
		stats.Archived++
	case "spam":
		if dryRun {
			stats.Spam++
			return
		}
		if _, err := gmailSvc.Users.Messages.Modify("me", messageID, &gmail.ModifyMessageRequest{AddLabelIds: []string{"SPAM"}, RemoveLabelIds: []string{"UNREAD", "INBOX"}}).Context(ctx).Do(); err != nil {
			log.Printf("gmail spam move failed (session=%s, message=%s): %v", checkoutSessionID, messageID, err)
			return
		}
		stats.Spam++
	case "keep":
		stats.Kept++
	default:
		return
	}
}

func newAIRateLimiter(requestsPerSec int) (<-chan struct{}, func()) {
	if requestsPerSec <= 0 {
		requestsPerSec = 1
	}
	interval := time.Second / time.Duration(requestsPerSec)
	if interval <= 0 {
		interval = time.Millisecond
	}

	tokens := make(chan struct{}, requestsPerSec)
	for i := 0; i < requestsPerSec; i++ {
		tokens <- struct{}{}
	}

	stop := make(chan struct{})
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				select {
				case tokens <- struct{}{}:
				default:
				}
			}
		}
	}()

	return tokens, func() { close(stop) }
}

func waitForAIRateToken(ctx context.Context, tokens <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-tokens:
		return nil
	}
}

func (s *Service) classifyWithRetry(ctx context.Context, input aiInput) (aiDecision, error) {
	delay := defaultAIInitialBackoff
	var lastErr error

	for attempt := 1; attempt <= defaultAIMaxRetries; attempt++ {
		decision, err := s.ai.Classify(ctx, input)
		if err == nil {
			return decision, nil
		}
		lastErr = err
		if !isRetryableAIError(err) || attempt == defaultAIMaxRetries {
			break
		}

		jitter := time.Duration(time.Now().UnixNano() % int64(delay/2+1))
		wait := delay + jitter
		select {
		case <-ctx.Done():
			return aiDecision{}, ctx.Err()
		case <-time.After(wait):
		}

		delay *= 2
		if delay > defaultAIMaxBackoff {
			delay = defaultAIMaxBackoff
		}
	}

	return aiDecision{}, lastErr
}

func isRetryableAIError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "status 429") ||
		strings.Contains(msg, "status 408") ||
		strings.Contains(msg, "status 500") ||
		strings.Contains(msg, "status 502") ||
		strings.Contains(msg, "status 503") ||
		strings.Contains(msg, "status 504") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "temporarily unavailable")
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
