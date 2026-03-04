package cleaner

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jkeddari/cleanmybox/internal/llm"
	"github.com/jkeddari/cleanmybox/internal/provider"
)

const (
	defaultPipelineWorkers  = 6
	defaultAIRequestsPerSec = 4
	defaultAIMaxRetries     = 5
	defaultAIInitialBackoff = 500 * time.Millisecond
	defaultAIMaxBackoff     = 8 * time.Second
)

type EngineConfig struct {
	LLM              llm.Client
	DryRun           bool
	PipelineWorkers  int
	AIRequestsPerSec int
}

type Engine struct {
	llm              llm.Client
	dryRun           bool
	pipelineWorkers  int
	aiRequestsPerSec int
}

type RunOptions struct {
	Plan              string
	CheckoutSessionID string
	Mailbox           provider.Provider
	OnStep            func(step string)
	OnInit            func(total int)
	OnProgress        func(processed, total int, currentEmail string, stats Stats)
}

type pipelineResult struct {
	currentEmail string
	stats        Stats
}

func NewEngine(cfg EngineConfig) *Engine {
	workers := cfg.PipelineWorkers
	if workers <= 0 {
		workers = defaultPipelineWorkers
	}
	requestsPerSec := cfg.AIRequestsPerSec
	if requestsPerSec <= 0 {
		requestsPerSec = defaultAIRequestsPerSec
	}

	return &Engine{
		llm:              cfg.LLM,
		dryRun:           cfg.DryRun,
		pipelineWorkers:  workers,
		aiRequestsPerSec: requestsPerSec,
	}
}

func (e *Engine) Run(ctx context.Context, opts RunOptions) (Stats, error) {
	if opts.Mailbox == nil {
		return Stats{}, errors.New("missing mailbox provider")
	}

	if opts.OnStep != nil {
		opts.OnStep("scanning_emails")
	}

	messageIDs, err := opts.Mailbox.ListInboxMessageIDs(ctx)
	if err != nil {
		return Stats{}, err
	}

	log.Printf("cleanup inbox size fetched (session=%s, total_inbox=%d, dry_run=%t)", opts.CheckoutSessionID, len(messageIDs), e.dryRun)
	log.Printf("cleanup scan started (session=%s, plan=%s, dry_run=%t)", opts.CheckoutSessionID, opts.Plan, e.dryRun)

	if opts.OnInit != nil {
		opts.OnInit(len(messageIDs))
	}

	cleanPlus := strings.EqualFold(opts.Plan, "cleanplus")
	if cleanPlus && e.llm == nil {
		return Stats{}, errors.New("cleanplus requested but OPENAI_API_KEY is missing")
	}

	var scanner *aiScan
	if cleanPlus {
		scanner = newAIScan(e, e.aiRequestsPerSec)
		defer scanner.Close()
	}

	stats := Stats{}
	if len(messageIDs) == 0 {
		return stats, nil
	}

	workerCount := e.pipelineWorkers
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
				result := e.processMessage(ctx, opts.Mailbox, opts.CheckoutSessionID, messageID, cleanPlus, scanner)
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
			log.Printf("cleanup progress (session=%s, processed=%d/%d)", opts.CheckoutSessionID, processed, len(messageIDs))
		}

		if opts.OnProgress != nil {
			opts.OnProgress(processed, len(messageIDs), firstNonEmpty(result.currentEmail, ""), stats)
		}
	}

	return stats, nil
}

func (e *Engine) processMessage(ctx context.Context, mailbox provider.Provider, checkoutSessionID, messageID string, cleanPlus bool, scanner *aiScan) pipelineResult {
	result := pipelineResult{}
	result.stats.TotalScanned = 1
	touched := false
	finalize := func() pipelineResult {
		if !touched {
			result.stats.Kept = 1
		}
		return result
	}

	msg, err := mailbox.GetMessage(ctx, messageID)
	if err != nil {
		result.stats.ScanFailed = 1
		return result
	}

	result.currentEmail = firstNonEmpty(strings.TrimSpace(msg.Subject), strings.TrimSpace(msg.From), "(no subject)")

	headers := msg.Headers
	if isNewsletter(headers) {
		result.stats.Newsletters = 1

		if e.dryRun {
			touched = true
			result.stats.Deleted = 1
		} else {
			if err := mailbox.MarkRead(ctx, msg.ID); err != nil {
				log.Printf("mailbox mark read failed (session=%s, message=%s): %v", checkoutSessionID, msg.ID, err)
			} else {
				touched = true
			}
			if err := mailbox.Delete(ctx, msg.ID); err == nil {
				touched = true
				result.stats.Deleted = 1
			} else {
				log.Printf("mailbox delete failed (session=%s, message=%s): %v", checkoutSessionID, msg.ID, err)
			}
		}

		if unsubHeader := headers["list-unsubscribe"]; unsubHeader != "" {
			if e.dryRun {
				log.Printf("dry-run unsubscribe skipped (session=%s, message=%s)", checkoutSessionID, msg.ID)
			} else {
				result.stats.Unsubscribed = 1
				if ok, reason := tryHTTPUnsubscribe(ctx, unsubHeader); !ok {
					result.stats.UnsubscribedFailed = 1
					log.Printf("unsubscribe failed (session=%s, message=%s, reason=%s)", checkoutSessionID, msg.ID, reason)
				}
			}
		}

		return finalize()
	}

	if !cleanPlus {
		return finalize()
	}

	result.stats.AIScanned = 1
	decision, err := scanner.Classify(ctx, llm.Input{
		Subject: msg.Subject,
		From:    msg.From,
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
		return finalize()
	}

	switch decision.Verdict {
	case "useless":
		result.stats.Useless = 1
	case "legit":
		result.stats.Legit = 1
	case "spam":
	default:
		result.stats.Unsure = 1
	}

	actionStats := Stats{}
	if e.applyAIDecisionAction(ctx, mailbox, checkoutSessionID, msg.ID, decision.Action, &actionStats) {
		touched = true
	}
	result.stats.Deleted += actionStats.Deleted
	result.stats.Archived += actionStats.Archived
	result.stats.Spam += actionStats.Spam

	return finalize()
}

func (e *Engine) applyAIDecisionAction(ctx context.Context, mailbox provider.Provider, checkoutSessionID, messageID, action string, stats *Stats) bool {
	switch action {
	case "delete":
		if e.dryRun {
			stats.Deleted++
			return true
		}
		touched := false
		if err := mailbox.MarkRead(ctx, messageID); err != nil {
			log.Printf("mailbox mark read failed (session=%s, message=%s): %v", checkoutSessionID, messageID, err)
		} else {
			touched = true
		}
		if err := mailbox.Delete(ctx, messageID); err != nil {
			log.Printf("mailbox delete failed (session=%s, message=%s): %v", checkoutSessionID, messageID, err)
			return touched
		}
		stats.Deleted++
		return true
	case "archive":
		if e.dryRun {
			stats.Archived++
			return true
		}
		if err := mailbox.Archive(ctx, messageID); err != nil {
			log.Printf("mailbox archive failed (session=%s, message=%s): %v", checkoutSessionID, messageID, err)
			return false
		}
		stats.Archived++
		return true
	case "spam":
		if e.dryRun {
			stats.Spam++
			return true
		}
		if err := mailbox.MoveToSpam(ctx, messageID); err != nil {
			log.Printf("mailbox spam move failed (session=%s, message=%s): %v", checkoutSessionID, messageID, err)
			return false
		}
		stats.Spam++
		return true
	}
	return false
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

type aiScan struct {
	tokens <-chan struct{}
	stop   func()
	engine *Engine
}

func newAIScan(engine *Engine, requestsPerSec int) *aiScan {
	tokens, stop := newAIRateLimiter(requestsPerSec)
	return &aiScan{tokens: tokens, stop: stop, engine: engine}
}

func (a *aiScan) Close() {
	if a == nil || a.stop == nil {
		return
	}
	a.stop()
	a.stop = nil
}

func (a *aiScan) Classify(ctx context.Context, input llm.Input) (llm.Decision, error) {
	if err := waitForAIRateToken(ctx, a.tokens); err != nil {
		return llm.Decision{}, err
	}
	return a.engine.classifyWithRetry(ctx, input)
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

func (e *Engine) classifyWithRetry(ctx context.Context, input llm.Input) (llm.Decision, error) {
	delay := defaultAIInitialBackoff
	var lastErr error

	for attempt := 1; attempt <= defaultAIMaxRetries; attempt++ {
		decision, err := e.llm.Classify(ctx, input)
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
			return llm.Decision{}, ctx.Err()
		case <-time.After(wait):
		}

		delay *= 2
		if delay > defaultAIMaxBackoff {
			delay = defaultAIMaxBackoff
		}
	}

	return llm.Decision{}, lastErr
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
