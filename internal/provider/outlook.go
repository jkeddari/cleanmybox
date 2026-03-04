package provider

import (
	"context"
	"errors"
)

var ErrNotImplemented = errors.New("provider not implemented")

type OutlookProvider struct {
	// TODO: add Microsoft Graph client and auth dependencies.
}

func NewOutlookProvider() (*OutlookProvider, error) {
	// TODO: initialize Outlook provider with OAuth token and Graph client.
	return nil, ErrNotImplemented
}

func (p *OutlookProvider) ListInboxMessageIDs(ctx context.Context) ([]string, error) {
	// TODO: list inbox message IDs through Microsoft Graph API.
	return nil, ErrNotImplemented
}

func (p *OutlookProvider) GetMessage(ctx context.Context, messageID string) (Message, error) {
	// TODO: fetch metadata message with headers/subject/from/snippet.
	return Message{}, ErrNotImplemented
}

func (p *OutlookProvider) MarkRead(ctx context.Context, messageID string) error {
	// TODO: mark Outlook message as read.
	return ErrNotImplemented
}

func (p *OutlookProvider) Delete(ctx context.Context, messageID string) error {
	// TODO: move Outlook message to deleted items.
	return ErrNotImplemented
}

func (p *OutlookProvider) Archive(ctx context.Context, messageID string) error {
	// TODO: archive Outlook message.
	return ErrNotImplemented
}

func (p *OutlookProvider) MoveToSpam(ctx context.Context, messageID string) error {
	// TODO: move Outlook message to junk folder.
	return ErrNotImplemented
}
