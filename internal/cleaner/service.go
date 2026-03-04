package cleaner

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jkeddari/cleanmybox/internal/history"
	"github.com/jkeddari/cleanmybox/internal/llm"
	"github.com/jkeddari/cleanmybox/internal/provider"
	"golang.org/x/oauth2"
)

type TokenProvider interface {
	TokenBySessionID(sessionID string) (*oauth2.Token, bool)
}

type Config struct {
	LLM              llm.Client
	DryRun           bool
	PipelineWorkers  int
	AIRequestsPerSec int
	History          history.Store
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
	UserEmail         string    `json:"-"`
}

const (
	jobStaleAfter      = 6 * time.Minute
	jobStallCheckEvery = 15 * time.Second
)

type Service struct {
	tokens  TokenProvider
	engine  *Engine
	dryRun  bool
	history history.Store

	mu   sync.Mutex
	jobs map[string]Job
}

func NewService(tokens TokenProvider, cfg Config) *Service {
	return &Service{
		tokens: tokens,
		engine: NewEngine(EngineConfig{
			LLM:              cfg.LLM,
			DryRun:           cfg.DryRun,
			PipelineWorkers:  cfg.PipelineWorkers,
			AIRequestsPerSec: cfg.AIRequestsPerSec,
		}),
		dryRun:  cfg.DryRun,
		history: cfg.History,
		jobs:    make(map[string]Job),
	}
}

func (s *Service) Launch(plan, checkoutSessionID, sessionRef, userEmail string) {
	job := Job{
		CheckoutSessionID: checkoutSessionID,
		Plan:              plan,
		Status:            StatusPending,
		Step:              "queued",
		ProgressPercent:   1,
		DryRun:            s.dryRun,
		StaleAfterSeconds: int(jobStaleAfter.Seconds()),
		UserEmail:         strings.TrimSpace(userEmail),
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

	mailbox, err := provider.NewGmailProvider(ctx, token)
	if err != nil {
		s.failJob(checkoutSessionID, fmt.Sprintf("gmail init failed: %v", err))
		return
	}

	stats, err := s.engine.Run(ctx, RunOptions{
		Plan:              plan,
		CheckoutSessionID: checkoutSessionID,
		Mailbox:           mailbox,
		OnStep: func(step string) {
			s.updateJob(checkoutSessionID, func(j *Job) {
				j.Step = step
			})
		},
		OnInit: func(total int) {
			s.updateJob(checkoutSessionID, func(j *Job) {
				j.Step = "processing_emails"
				j.TotalCount = total
				j.ProcessedCount = 0
				j.ProgressPercent = percentFromCounts(0, total)
			})
		},
		OnProgress: func(processed, total int, currentEmail string, snapshot Stats) {
			s.updateJob(checkoutSessionID, func(j *Job) {
				j.Step = "processing_emails"
				j.TotalCount = total
				j.ProcessedCount = processed
				j.CurrentEmail = currentEmail
				j.Stats = snapshot
				j.ProgressPercent = percentFromCounts(processed, total)
			})
		},
	})
	if err != nil {
		s.failJob(checkoutSessionID, err.Error())
		return
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
	s.persistHistory(checkoutSessionID)

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
	s.persistHistory(checkoutSessionID)
	log.Printf("cleanup failed (session=%s): %s", checkoutSessionID, errMsg)
}

func (s *Service) persistHistory(checkoutSessionID string) {
	if s.history == nil {
		return
	}

	job, ok := s.Job(checkoutSessionID)
	if !ok {
		return
	}
	if job.Status != StatusDone && job.Status != StatusError {
		return
	}

	durationSeconds := 0
	if !job.StartedAt.IsZero() {
		end := job.FinishedAt
		if end.IsZero() {
			end = time.Now()
		}
		if end.After(job.StartedAt) {
			durationSeconds = int(end.Sub(job.StartedAt).Seconds())
		}
	}

	record := history.RunRecord{
		CheckoutSessionID: job.CheckoutSessionID,
		UserEmail:         firstNonEmpty(strings.TrimSpace(job.UserEmail), "unknown"),
		Plan:              strings.ToLower(strings.TrimSpace(job.Plan)),
		Status:            string(job.Status),
		DryRun:            job.DryRun,
		StartedAtUnix:     safeUnix(job.StartedAt),
		FinishedAtUnix:    safeUnix(job.FinishedAt),
		DurationSeconds:   durationSeconds,
		TotalCount:        job.TotalCount,
		ProcessedCount:    job.ProcessedCount,
		ProgressPercent:   job.ProgressPercent,
		Error:             strings.TrimSpace(job.Error),
		Stats: history.Stats{
			TotalScanned:       job.Stats.TotalScanned,
			AIScanned:          job.Stats.AIScanned,
			ScanFailed:         job.Stats.ScanFailed,
			Newsletters:        job.Stats.Newsletters,
			Spam:               job.Stats.Spam,
			Useless:            job.Stats.Useless,
			Legit:              job.Stats.Legit,
			Unsure:             job.Stats.Unsure,
			Archived:           job.Stats.Archived,
			Kept:               job.Stats.Kept,
			Deleted:            job.Stats.Deleted,
			Unsubscribed:       job.Stats.Unsubscribed,
			UnsubscribedFailed: job.Stats.UnsubscribedFailed,
		},
		CreatedAtUnix: time.Now().Unix(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.history.SaveRun(ctx, record); err != nil {
		log.Printf("history save failed (session=%s): %v", checkoutSessionID, err)
	}
}

func safeUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
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
