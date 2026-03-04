package llm

import "context"

type Input struct {
	Subject string            `json:"subject"`
	From    string            `json:"from"`
	Snippet string            `json:"snippet"`
	Headers map[string]string `json:"headers"`
}

type Decision struct {
	Action  string `json:"action"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

type Client interface {
	Classify(ctx context.Context, input Input) (Decision, error)
}
