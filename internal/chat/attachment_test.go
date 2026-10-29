package chat

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"os"
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

func TestSessionStoresUsefulAttachmentOnlyHistoryPlaceholder(t *testing.T) {
	session := NewSession(Config{PrimaryModel: DefaultPrimaryModel, FallbackModel: DefaultFallbackModel}, providerFunc(func(context.Context, string, string) (string, error) {
		return "received", nil
	}))
	metadata := attachment.Metadata{
		Type:     "image",
		Filename: "photo.png",
		Path:     filepath.Join("/tmp", "goaster-attachment-placeholder", "photo.png"),
		MIMEType: "image/png",
		Size:     10,
	}
	if _, err := session.ReplyWithAttachments(context.Background(), "", []attachment.Metadata{metadata}); err != nil {
		t.Fatal(err)
	}
	if len(session.history) < 2 || session.history[0].Content == "" || !strings.Contains(session.history[0].Content, "photo.png") {
		t.Fatalf("stored attachment-only history = %#v", session.history)
	}
	if strings.Contains(session.history[0].Content, metadata.Path) || strings.Contains(session.history[0].Content, "base64") {
		t.Fatalf("stored history retained temporary data: %#v", session.history[0])
	}
}

func TestSessionDoesNotPersistEchoedAttachmentPathOrImageData(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	pathName := filepath.Join(directory, "photo.png")
	if err := os.WriteFile(pathName, imageData.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(imageData.Bytes())
	provider := providerFunc(func(context.Context, string, string) (string, error) {
		return "seen at " + pathName + " data:image/png;base64," + encoded + " and " + encoded, nil
	})
	storePath := filepath.Join(t.TempDir(), "session.json")
	session, err := NewSessionWithStore(Config{PrimaryModel: "primary"}, provider, NewSessionStoreAt(storePath))
	if err != nil {
		t.Fatal(err)
	}
	metadata := attachment.Metadata{Type: "image", Filename: "photo.png", Path: pathName, MIMEType: "image/png", Size: int64(imageData.Len())}
	reply, err := session.ReplyWithAttachments(context.Background(), "inspect", []attachment.Metadata{metadata})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{reply, string(stored)} {
		if strings.Contains(output, pathName) || strings.Contains(output, encoded) {
			t.Fatalf("attachment path or image data escaped the turn: %q", output)
		}
	}
}

func TestSessionRedactsEchoedAgentAttachmentDirectory(t *testing.T) {
	directory := filepath.Join(os.TempDir(), attachment.AgentTemporaryDirectoryPrefix+"example")
	metadata := attachment.Metadata{Type: "document", Filename: "notes.txt", Path: filepath.Join(directory, "notes.txt"), Size: 1}
	provider := providerFunc(func(context.Context, string, string) (string, error) {
		return "directory: " + directory + "; file: " + metadata.Path, nil
	})
	session := NewSession(Config{PrimaryModel: "primary"}, provider)
	reply, err := session.ReplyWithAttachments(context.Background(), "read", []attachment.Metadata{metadata})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply, directory) || strings.Contains(session.history[len(session.history)-1].Content, directory) {
		t.Fatalf("temporary directory escaped the turn: %q", reply)
	}
}
