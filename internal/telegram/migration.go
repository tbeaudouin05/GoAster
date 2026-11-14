package telegram

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"goaster/internal/diagnostics"
)

const migrationHandoffFilename = "goaster-migration-handoff.txt"
const migrationHandoffMaxBytes int64 = 32 * 1024

func migrationTelegramClient() httpDoer {
	return &http.Client{
		Timeout:       40 * time.Second,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: noRedirect,
	}
}

// PrepareMigrationHandoff fixes the paired recipient before a create request.
func PrepareMigrationHandoff() (func(context.Context, []byte) error, error) {
	return prepareMigrationHandoffWith(DefaultPaths, migrationTelegramClient(), "https://api.telegram.org")
}

func prepareMigrationHandoffWith(paths Paths, client httpDoer, apiBase string) (func(context.Context, []byte) error, error) {
	userData, err := readPrivate(paths.UserID)
	if err != nil {
		return nil, errors.New("migration paired user is unavailable")
	}
	defer clear(userData)
	userID, err := strconv.ParseInt(string(userData), 10, 64)
	if err != nil || userID <= 0 {
		return nil, errors.New("migration paired user is unavailable")
	}
	token, err := readPrivate(paths.Token)
	if err != nil {
		return nil, errors.New("migration Telegram token is unavailable")
	}
	defer clear(token)
	if err := ValidateToken(string(token)); err != nil {
		return nil, errors.New("migration Telegram token is unavailable")
	}
	return func(ctx context.Context, data []byte) error {
		return sendMigrationHandoffTo(ctx, paths, client, apiBase, userID, data)
	}, nil
}

func sendMigrationHandoffTo(ctx context.Context, paths Paths, client httpDoer, apiBase string, userID int64, data []byte) error {
	if len(data) == 0 || int64(len(data)) > migrationHandoffMaxBytes {
		return errors.New("migration handoff is unavailable")
	}
	token, err := readPrivate(paths.Token)
	if err != nil {
		diagnostics.Error("telegram.migration_delivery", err, diagnostics.Field{Key: "method", Value: "sendDocument"})
		return errors.New("migration handoff is unavailable")
	}
	defer clear(token)
	if err := ValidateToken(string(token)); err != nil {
		diagnostics.Error("telegram.migration_delivery", err, diagnostics.Field{Key: "method", Value: "sendDocument"})
		return errors.New("migration handoff is unavailable")
	}
	if err := sendDocument(ctx, client, apiBase, string(token), userID, migrationHandoffFilename, data); err != nil {
		return errors.New("migration handoff delivery failed")
	}
	return nil
}
