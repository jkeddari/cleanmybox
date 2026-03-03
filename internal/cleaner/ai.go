package cleaner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type aiInput struct {
	Subject string            `json:"subject"`
	From    string            `json:"from"`
	Snippet string            `json:"snippet"`
	Headers map[string]string `json:"headers"`
}

type aiDecision struct {
	Action  string `json:"action"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

type openAIClient struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

func newOpenAIClient(apiKey, baseURL, model string) *openAIClient {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.openai.com"
	}
	if strings.TrimSpace(model) == "" {
		model = "gpt-4o-mini"
	}

	return &openAIClient{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{Timeout: 18 * time.Second},
	}
}

func (c *openAIClient) Classify(ctx context.Context, input aiInput) (aiDecision, error) {
	reqBody := map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You are a conservative email triage agent. Return only valid JSON with shape: {\"action\":\"delete|archive|spam|keep\",\"verdict\":\"spam|useless|legit|unsure\",\"reason\":\"short reason\"}. Rules: spam/phishing/malicious -> action=spam verdict=spam; useless newsletters/promotional noise -> action=delete verdict=useless; useful but old and no longer actionable -> action=archive verdict=legit; uncertain or potentially important -> action=keep verdict=unsure. Never output text outside JSON.",
			},
			{
				"role":    "user",
				"content": buildUserPrompt(input),
			},
		},
		"temperature": 0,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return aiDecision{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return aiDecision{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return aiDecision{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return aiDecision{}, err
	}

	if resp.StatusCode >= 300 {
		return aiDecision{}, fmt.Errorf("openai status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return aiDecision{}, err
	}
	if len(parsed.Choices) == 0 {
		return aiDecision{}, errors.New("openai returned no choices")
	}

	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		content = content[start : end+1]
	}

	var decision aiDecision
	if err := json.Unmarshal([]byte(content), &decision); err != nil {
		return aiDecision{}, err
	}

	decision.Action = strings.ToLower(strings.TrimSpace(decision.Action))
	decision.Verdict = strings.ToLower(strings.TrimSpace(decision.Verdict))

	switch decision.Action {
	case "delete", "archive", "spam", "keep":
	default:
		decision.Action = "keep"
	}

	switch decision.Verdict {
	case "spam", "useless", "legit", "unsure":
	default:
		decision.Verdict = verdictFromAction(decision.Action)
	}

	if decision.Action == "spam" {
		decision.Verdict = "spam"
	} else if decision.Action == "delete" && decision.Verdict == "legit" {
		decision.Verdict = "useless"
	} else if decision.Action == "archive" && decision.Verdict == "spam" {
		decision.Verdict = "legit"
	}

	return decision, nil
}

func buildUserPrompt(input aiInput) string {
	raw, _ := json.Marshal(input)
	return "Decide the safest action for this email:\n" + string(raw)
}

func verdictFromAction(action string) string {
	switch action {
	case "spam":
		return "spam"
	case "delete":
		return "useless"
	case "archive":
		return "legit"
	default:
		return "unsure"
	}
}
