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

// SendMigrationHandoff sends only the protected migration document to the
// already paired positive Telegram user. It accepts no model-selected chat ID.
func SendMigrationHandoff(ctx context.Context, data []byte) error {
	return sendMigrationHandoffWith(ctx, DefaultPaths, migrationTelegramClient(), "https://api.telegram.org", data)
}

func migrationTelegramClient() httpDoer {
	return &http.Client{
		Timeout:       40 * time.Second,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: noRedirect,
	}
}

// CheckMigrationDelivery validates the protected token and paired user files
// without contacting Telegram or exposing either value.
func CheckMigrationDelivery() error {
	token, err := readPrivate(DefaultPaths.Token)
	if err != nil {
		return errors.New("migration Telegram token is unavailable")
	}
	defer clear(token)
	if err := ValidateToken(string(token)); err != nil {
		return errors.New("migration Telegram token is unavailable")
	}
	userData, err := readPrivate(DefaultPaths.UserID)
	if err != nil {
		return errors.New("migration paired user is unavailable")
	}
	defer clear(userData)
	userID, err := strconv.ParseInt(string(userData), 10, 64)
	if err != nil || userID <= 0 {
		return errors.New("migration paired user is unavailable")
	}
	return nil
}

func sendMigrationHandoffWith(ctx context.Context, paths Paths, client httpDoer, apiBase string, data []byte) error {
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
	userData, err := readPrivate(paths.UserID)
	if err != nil {
		diagnostics.Error("telegram.migration_delivery", err, diagnostics.Field{Key: "method", Value: "sendDocument"})
		return errors.New("migration handoff is unavailable")
	}
	defer clear(userData)
	userID, err := strconv.ParseInt(string(userData), 10, 64)
	if err != nil || userID <= 0 {
		diagnostics.Error("telegram.migration_delivery", errors.New("paired Telegram user is invalid"), diagnostics.Field{Key: "method", Value: "sendDocument"})
		return errors.New("migration handoff is unavailable")
	}
	if err := sendDocument(ctx, client, apiBase, string(token), userID, migrationHandoffFilename, data); err != nil {
		return errors.New("migration handoff delivery failed")
	}
	return nil
}
