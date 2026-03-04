package provider

import (
	"context"
	"strings"

	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

type GmailProvider struct {
	svc *gmail.Service
}

func NewGmailProvider(ctx context.Context, token *oauth2.Token) (*GmailProvider, error) {
	svc, err := gmail.NewService(ctx, option.WithTokenSource(oauth2.StaticTokenSource(token)))
	if err != nil {
		return nil, err
	}
	return &GmailProvider{svc: svc}, nil
}

func (p *GmailProvider) ListInboxMessageIDs(ctx context.Context) ([]string, error) {
	ids := make([]string, 0)
	nextPageToken := ""

	for {
		req := p.svc.Users.Messages.List("me").Q("in:inbox").MaxResults(100).Context(ctx)
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

func (p *GmailProvider) GetMessage(ctx context.Context, messageID string) (Message, error) {
	msg, err := p.svc.Users.Messages.Get("me", messageID).
		Format("metadata").
		MetadataHeaders("List-Unsubscribe", "List-Id", "Precedence", "Auto-Submitted", "From", "Subject").
		Context(ctx).
		Do()
	if err != nil {
		return Message{}, err
	}

	headers := map[string]string{}
	if msg.Payload != nil {
		for _, h := range msg.Payload.Headers {
			headers[strings.ToLower(strings.TrimSpace(h.Name))] = strings.TrimSpace(h.Value)
		}
	}

	return Message{
		ID:      msg.Id,
		Subject: strings.TrimSpace(headers["subject"]),
		From:    strings.TrimSpace(headers["from"]),
		Snippet: msg.Snippet,
		Headers: headers,
	}, nil
}

func (p *GmailProvider) MarkRead(ctx context.Context, messageID string) error {
	_, err := p.svc.Users.Messages.Modify("me", messageID, &gmail.ModifyMessageRequest{RemoveLabelIds: []string{"UNREAD"}}).Context(ctx).Do()
	return err
}

func (p *GmailProvider) Delete(ctx context.Context, messageID string) error {
	_, err := p.svc.Users.Messages.Trash("me", messageID).Context(ctx).Do()
	return err
}

func (p *GmailProvider) Archive(ctx context.Context, messageID string) error {
	_, err := p.svc.Users.Messages.Modify("me", messageID, &gmail.ModifyMessageRequest{RemoveLabelIds: []string{"UNREAD", "INBOX"}}).Context(ctx).Do()
	return err
}

func (p *GmailProvider) MoveToSpam(ctx context.Context, messageID string) error {
	_, err := p.svc.Users.Messages.Modify("me", messageID, &gmail.ModifyMessageRequest{AddLabelIds: []string{"SPAM"}, RemoveLabelIds: []string{"UNREAD", "INBOX"}}).Context(ctx).Do()
	return err
}
