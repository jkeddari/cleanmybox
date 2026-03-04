package provider

import "context"

type Message struct {
	ID      string
	Subject string
	From    string
	Snippet string
	Headers map[string]string
}

type Provider interface {
	ListInboxMessageIDs(ctx context.Context) ([]string, error)
	GetMessage(ctx context.Context, messageID string) (Message, error)
	MarkRead(ctx context.Context, messageID string) error
	Delete(ctx context.Context, messageID string) error
	Archive(ctx context.Context, messageID string) error
	MoveToSpam(ctx context.Context, messageID string) error
}
