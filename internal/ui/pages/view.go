package pages

type JobStatsView struct {
	Deleted            int
	Kept               int
	AIScanned          int
	ScanFailed         int
	Newsletters        int
	Spam               int
	Useless            int
	Legit              int
	Unsure             int
	Archived           int
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
	ScanDuration    string
	Error           string
	Stats           JobStatsView
}

type HistoryRunView struct {
	CreatedAt       string
	Plan            string
	Status          string
	StatusLabel     string
	Duration        string
	DryRun          bool
	Error           string
	CheckoutSession string
	Stats           JobStatsView
}

type HistoryView struct {
	Email         string
	Runs          []HistoryRunView
	TotalRuns     int
	SuccessRuns   int
	FailedRuns    int
	SuccessRate   int
	DeletedTotal  int
	ArchivedTotal int
}
