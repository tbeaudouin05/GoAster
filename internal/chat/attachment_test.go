package chat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/attachment"
)

type attachmentHistoryProvider struct {
	histories [][]ChatMessage
}

func (p *attachmentHistoryProvider) Reply(_ context.Context, _ string, message string) (string, error) {
	return "reply to " + message, nil
}

func (p *attachmentHistoryProvider) ReplyWithHistory(_ context.Context, _ string, history []ChatMessage) (string, error) {
	p.histories = append(p.histories, append([]ChatMessage(nil), history...))
	return "reply", nil
}

func TestSessionAttachmentReferencesAreCurrentTurnOnly(t *testing.T) {
	provider := &attachmentHistoryProvider{}
	session := NewSession(Config{PrimaryModel: DefaultPrimaryModel, FallbackModel: DefaultFallbackModel}, provider)
	metadata := attachment.Metadata{
		Type:     "document",
		Filename: "notes.txt",
		Path:     filepath.Join("/tmp", "goaster-telegram-attachment-test", "notes.txt"),
		MIMEType: "text/plain",
		Size:     17,
	}
	if _, err := session.ReplyWithAttachments(context.Background(), "read this", []attachment.Metadata{metadata}); err != nil {
		t.Fatal(err)
	}
	if len(provider.histories) != 1 || len(provider.histories[0]) != 1 {
		t.Fatalf("first provider history = %#v", provider.histories)
	}
	for _, want := range []string{"read this", "notes.txt", metadata.Path, "17"} {
		if !strings.Contains(provider.histories[0][0].Content, want) {
			t.Errorf("provider input missing %q: %q", want, provider.histories[0][0].Content)
		}
	}

	if _, err := session.Reply(context.Background(), "next message"); err != nil {
		t.Fatal(err)
	}
	if len(provider.histories) != 2 {
		t.Fatalf("provider histories = %#v", provider.histories)
	}
	for _, message := range provider.histories[1] {
		if strings.Contains(message.Content, metadata.Path) {
			t.Fatalf("temporary attachment path persisted into the next turn: %#v", provider.histories[1])
		}
	}
}

func TestSessionRejectsTooManyOrUnsafeAttachmentReferences(t *testing.T) {
	session := NewSession(Config{PrimaryModel: DefaultPrimaryModel, FallbackModel: DefaultFallbackModel}, providerFunc(func(context.Context, string, string) (string, error) {
		return "unused", nil
	}))
	metadata := attachment.Metadata{Type: "image", Filename: "photo.jpg", Path: "/tmp/photo.jpg", Size: 1}
	if _, err := session.ReplyWithAttachments(context.Background(), "", []attachment.Metadata{metadata, metadata}); err == nil {
		t.Fatal("accepted more than one attachment")
	}
	metadata.Filename = "../photo.jpg"
	if _, err := session.ReplyWithAttachments(context.Background(), "", []attachment.Metadata{metadata}); err == nil {
		t.Fatal("accepted an unsafe attachment filename")
	}
}
