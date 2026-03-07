package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

type OutlookProvider struct {
	client *http.Client
}

func NewOutlookProvider(ctx context.Context, token *oauth2.Token) (*OutlookProvider, error) {
	if token == nil {
		return nil, fmt.Errorf("missing outlook token")
	}
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(token))
	client.Timeout = 20 * time.Second

	return &OutlookProvider{client: client}, nil
}

func (p *OutlookProvider) ListInboxMessageIDs(ctx context.Context) ([]string, error) {
	nextURL := "https://graph.microsoft.com/v1.0/me/mailFolders/inbox/messages?$select=id&$top=50"
	ids := make([]string, 0)

	for strings.TrimSpace(nextURL) != "" {
		body, err := p.doJSON(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}

		var page struct {
			Value []struct {
				ID string `json:"id"`
			} `json:"value"`
			Next string `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}

		for _, item := range page.Value {
			if strings.TrimSpace(item.ID) != "" {
				ids = append(ids, item.ID)
			}
		}
		nextURL = strings.TrimSpace(page.Next)
	}

	return ids, nil
}

func (p *OutlookProvider) GetMessage(ctx context.Context, messageID string) (Message, error) {
	body, err := p.doJSON(ctx, http.MethodGet, "https://graph.microsoft.com/v1.0/me/messages/"+url.PathEscape(messageID)+"?$select=id,subject,from,bodyPreview,internetMessageHeaders", nil)
	if err != nil {
		return Message{}, err
	}

	var msg struct {
		ID          string `json:"id"`
		Subject     string `json:"subject"`
		BodyPreview string `json:"bodyPreview"`
		From        struct {
			EmailAddress struct {
				Address string `json:"address"`
			} `json:"emailAddress"`
		} `json:"from"`
		InternetMessageHeaders []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"internetMessageHeaders"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return Message{}, err
	}

	headers := make(map[string]string, len(msg.InternetMessageHeaders))
	for _, h := range msg.InternetMessageHeaders {
		name := strings.ToLower(strings.TrimSpace(h.Name))
		if name == "" {
			continue
		}
		headers[name] = strings.TrimSpace(h.Value)
	}

	return Message{
		ID:      strings.TrimSpace(msg.ID),
		Subject: strings.TrimSpace(msg.Subject),
		From:    strings.TrimSpace(msg.From.EmailAddress.Address),
		Snippet: strings.TrimSpace(msg.BodyPreview),
		Headers: headers,
	}, nil
}

func (p *OutlookProvider) MarkRead(ctx context.Context, messageID string) error {
	_, err := p.doJSON(ctx, http.MethodPatch, "https://graph.microsoft.com/v1.0/me/messages/"+url.PathEscape(messageID), map[string]any{"isRead": true})
	return err
}

func (p *OutlookProvider) Delete(ctx context.Context, messageID string) error {
	return p.doNoContent(ctx, http.MethodDelete, "https://graph.microsoft.com/v1.0/me/messages/"+url.PathEscape(messageID), nil)
}

func (p *OutlookProvider) Archive(ctx context.Context, messageID string) error {
	_, err := p.doJSON(ctx, http.MethodPost, "https://graph.microsoft.com/v1.0/me/messages/"+url.PathEscape(messageID)+"/move", map[string]any{"destinationId": "archive"})
	return err
}

func (p *OutlookProvider) MoveToSpam(ctx context.Context, messageID string) error {
	_, err := p.doJSON(ctx, http.MethodPost, "https://graph.microsoft.com/v1.0/me/messages/"+url.PathEscape(messageID)+"/move", map[string]any{"destinationId": "junkemail"})
	return err
}

func (p *OutlookProvider) doJSON(ctx context.Context, method, rawURL string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("outlook graph %s %s failed: status=%d body=%s", method, rawURL, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	return raw, nil
}

func (p *OutlookProvider) doNoContent(ctx context.Context, method, rawURL string, payload any) error {
	_, err := p.doJSON(ctx, method, rawURL, payload)
	return err
}
