package pages

type JobStatsView struct {
	Deleted            int
	Newsletters        int
	Spam               int
	Useless            int
	Legit              int
	Unsure             int
	AIDeleted          int
	TotalScanned       int
	UnsubscribedOK     int
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
	Error           string
	Stats           JobStatsView
}
