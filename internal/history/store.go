package history

import "context"

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

type RunRecord struct {
	CheckoutSessionID string
	UserEmail         string
	Plan              string
	Status            string
	DryRun            bool
	StartedAtUnix     int64
	FinishedAtUnix    int64
	DurationSeconds   int
	TotalCount        int
	ProcessedCount    int
	ProgressPercent   int
	Error             string
	Stats             Stats
	CreatedAtUnix     int64
}

type Store interface {
	SaveRun(ctx context.Context, record RunRecord) error
	ListRunsByEmail(ctx context.Context, email string, limit int) ([]RunRecord, error)
}
