package pages

type JobStatsView struct {
	Deleted            int
	ScanFailed         int
	Newsletters        int
	Spam               int
	Useless            int
	Legit              int
	Unsure             int
	AIDeleted          int
	Unsubscribed       int
	TotalScanned       int
	UnsubscribedFailed int
}

type JobStatusView struct {
	SessionID       string
	Found           bool
	Plan            string
	Status          string
	Step            string
	ProgressPercent int
	ProcessedCount  int
	TotalCount      int
	CurrentEmail    string
	DryRun          bool
	HeartbeatAgeSec int
	Stale           bool
	StaleAfterSec   int
	ScanDuration    string
	Error           string
	Stats           JobStatsView
}
