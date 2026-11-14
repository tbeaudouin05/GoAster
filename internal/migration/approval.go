package migration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"goaster/internal/diagnostics"
)

const (
	ApprovalCommandPrefix = "APPROVE GOASTER MIGRATION "
	approvalCodePrefix    = "migration-approval-"
	approvalCodeBytes     = 16
	approvalLifetime      = 10 * time.Minute
	approvalMessageLimit  = 4 * 1024
)

// ApprovalSubject is the identity asserted by the already authenticated
// Telegram adapter. Private approvals require the Telegram private-chat
// invariant that the user and chat IDs are the same positive ID.
type ApprovalSubject struct {
	UserID  int64
	ChatID  int64
	Private bool
}

type approvalSubjectContextKey struct{}

// WithApprovalSubject carries adapter-authenticated Telegram identity to the
// model tool without exposing it as a model-controlled tool argument.
func WithApprovalSubject(ctx context.Context, subject ApprovalSubject) context.Context {
	return context.WithValue(ctx, approvalSubjectContextKey{}, subject)
}

// ApprovalSubjectFromContext returns only an identity supplied by the local
// Telegram adapter. Missing or invalid identity is not an approval.
func ApprovalSubjectFromContext(ctx context.Context) (ApprovalSubject, bool) {
	if ctx == nil {
		return ApprovalSubject{}, false
	}
	subject, ok := ctx.Value(approvalSubjectContextKey{}).(ApprovalSubject)
	return subject, ok && subject.valid()
}

func (subject ApprovalSubject) valid() bool {
	return subject.Private && subject.UserID > 0 && subject.ChatID == subject.UserID
}

// ApprovalNotice is the non-secret, source-generated notice delivered
// directly to the paired Telegram private chat. Code is intentionally not
// returned by the model-facing migration tool.
type ApprovalNotice struct {
	Code     string
	Request  PriceRequest
	Estimate PriceEstimate
}

// ApprovalDelivery sends one source-generated notice to the protected paired
// Telegram user. It must not select a destination from model input.
type ApprovalDelivery func(context.Context, ApprovalNotice) error

// ApprovalCommand is the trusted adapter-to-agent approval event.
type ApprovalCommand struct {
	Code    string
	Subject ApprovalSubject
}

// ApprovalResult is safe for the Telegram adapter to render.
type ApprovalResult struct {
	Status string `json:"status"`
}

// ApprovalController connects model-triggered approval requests to one
// trusted paired-private approval event. Its closures retain state in memory;
// process restart therefore loses pending approval and fails closed.
type ApprovalController struct {
	Start   func(context.Context, Request) (Result, error)
	Approve func(context.Context, ApprovalCommand) (ApprovalResult, error)
}

type approvalProposal struct {
	code      string
	subject   ApprovalSubject
	request   Request
	expiresAt time.Time
	approved  bool
}

// NewApprovalController creates a bounded one-shot approval boundary. quote
// supplies informational pricing for the exact target request when available;
// execute receives only the request reconstructed from the explicit approval.
func NewApprovalController(
	now func() time.Time,
	quote func(context.Context, PriceRequest) (PriceEstimate, error),
	deliver ApprovalDelivery,
	execute func(context.Context, Request) (Result, error),
) ApprovalController {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	var approvalsMu sync.Mutex
	proposals := make(map[string]approvalProposal)
	bySubject := make(map[ApprovalSubject]string)

	removeProposal := func(code string) {
		proposal, ok := proposals[code]
		if !ok {
			return
		}
		delete(proposals, code)
		if bySubject[proposal.subject] == code {
			delete(bySubject, proposal.subject)
		}
	}
	removeExpired := func(at time.Time) {
		for code, proposal := range proposals {
			if !at.Before(proposal.expiresAt) {
				removeProposal(code)
			}
		}
	}

	start := func(ctx context.Context, request Request) (Result, error) {
		if ctx == nil {
			return Result{}, errors.New("migration approval context is unavailable")
		}
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if validateMachineRequest(request.ServerType, request.Locations) != nil || request.JobID != "" || request.ApprovedHourlyGross != "" || request.ApprovedCurrency != "" {
			return Result{Status: "rejected", ServerType: request.ServerType}, nil
		}
		subject, ok := ApprovalSubjectFromContext(ctx)
		if !ok {
			return Result{Status: "approval_required", ServerType: request.ServerType}, nil
		}

		at := currentTime(now)
		approvalsMu.Lock()
		removeExpired(at)
		if code := bySubject[subject]; code != "" {
			proposal := proposals[code]
			if samePriceRequest(PriceRequest{ServerType: proposal.request.ServerType, Locations: proposal.request.Locations}, PriceRequest{ServerType: request.ServerType, Locations: request.Locations}) {
				if proposal.approved {
					removeProposal(code)
					approvalsMu.Unlock()
					if execute == nil {
						return Result{Status: "manual_review", ServerType: request.ServerType, ManualReview: true}, errors.New("migration execution capability is unavailable")
					}
					return execute(ctx, proposal.request)
				}
				approvalsMu.Unlock()
				return Result{Status: "approval_required", ServerType: request.ServerType}, nil
			}
			removeProposal(code)
		}
		approvalsMu.Unlock()

		if deliver == nil {
			return Result{Status: "manual_review", ServerType: request.ServerType, ManualReview: true}, errors.New("migration approval capability is unavailable")
		}
		priceRequest := PriceRequest{ServerType: request.ServerType, Locations: append([]string(nil), request.Locations...)}
		estimate := unavailablePriceEstimate(priceRequest)
		if quote != nil {
			candidate, quoteErr := quote(ctx, priceRequest)
			if quoteErr != nil {
				if errors.Is(quoteErr, context.Canceled) || errors.Is(quoteErr, context.DeadlineExceeded) {
					return Result{}, fmt.Errorf("obtain migration approval estimate: %w", quoteErr)
				}
				diagnostics.Error("migration.approval.estimate", quoteErr)
			} else if !validApprovalEstimate(priceRequest, candidate) {
				diagnostics.Error("migration.approval.estimate", errors.New("migration approval estimate is unavailable"))
			} else {
				estimate = normalizeApprovalEstimate(priceRequest, candidate)
			}
		}
		code, err := newApprovalCode()
		if err != nil {
			return Result{}, fmt.Errorf("generate migration approval code: %w", err)
		}
		notice := ApprovalNotice{
			Code:     code,
			Request:  PriceRequest{ServerType: request.ServerType, Locations: append([]string(nil), request.Locations...)},
			Estimate: estimate,
		}
		proposal := approvalProposal{
			code:    code,
			subject: subject,
			request: Request{
				ServerType: request.ServerType,
				Locations:  append([]string(nil), request.Locations...),
			},
			expiresAt: at.Add(approvalLifetime),
		}
		approvalsMu.Lock()
		removeExpired(at)
		if previous := bySubject[subject]; previous != "" {
			removeProposal(previous)
		}
		proposals[code] = proposal
		bySubject[subject] = code
		approvalsMu.Unlock()

		if err := ctx.Err(); err != nil {
			approvalsMu.Lock()
			if proposals[code].code == code {
				removeProposal(code)
			}
			approvalsMu.Unlock()
			return Result{}, err
		}
		if err := deliver(ctx, notice); err != nil {
			approvalsMu.Lock()
			if proposals[code].code == code {
				removeProposal(code)
			}
			approvalsMu.Unlock()
			return Result{Status: "manual_review", ServerType: request.ServerType, ManualReview: true}, fmt.Errorf("deliver migration approval request: %w", err)
		}
		return Result{Status: "approval_required", ServerType: request.ServerType}, nil
	}

	approve := func(ctx context.Context, command ApprovalCommand) (ApprovalResult, error) {
		if ctx == nil {
			return ApprovalResult{}, errors.New("migration approval context is unavailable")
		}
		if err := ctx.Err(); err != nil {
			return ApprovalResult{}, err
		}
		if !command.Subject.valid() || !validApprovalCode(command.Code) {
			return ApprovalResult{Status: "rejected"}, nil
		}
		at := currentTime(now)
		approvalsMu.Lock()
		defer approvalsMu.Unlock()
		removeExpired(at)
		proposal, ok := proposals[command.Code]
		if !ok || proposal.subject != command.Subject || proposal.approved {
			return ApprovalResult{Status: "rejected"}, nil
		}
		proposal.approved = true
		proposals[command.Code] = proposal
		return ApprovalResult{Status: "approved"}, nil
	}

	return ApprovalController{Start: start, Approve: approve}
}

// ApprovalCode extracts only the exact, non-whitespace approval phrase used
// by the paired private Telegram adapter. It deliberately does not trim.
func ApprovalCode(text string) (string, bool) {
	if !strings.HasPrefix(text, ApprovalCommandPrefix) {
		return "", false
	}
	code := strings.TrimPrefix(text, ApprovalCommandPrefix)
	if !validApprovalCode(code) {
		return "", false
	}
	return code, true
}

// ApprovalMessage renders the source-generated target and informational terms
// shown to the owner. The one-time code is bound to the exact target request
// and one-create approval in controller state; pricing does not authorize or
// constrain the create.
func ApprovalMessage(notice ApprovalNotice) (string, error) {
	if !validApprovalCode(notice.Code) || !validApprovalEstimate(notice.Request, notice.Estimate) {
		return "", errors.New("migration approval notice is invalid")
	}
	var message strings.Builder
	message.WriteString("GoAster central migration approval request.\n")
	message.WriteString(fmt.Sprintf("Server type: %s\n", notice.Request.ServerType))
	message.WriteString("Strict locations: ")
	if len(notice.Request.Locations) == 0 {
		message.WriteString("any eligible location")
	} else {
		message.WriteString(strings.Join(notice.Request.Locations, ", "))
	}
	message.WriteByte('\n')
	if notice.Estimate.Status == "quoted" {
		message.WriteString("Pricing is informational only; no price cap or location ranking is applied.\n")
		message.WriteString(fmt.Sprintf("Currency: %s\n", notice.Estimate.Currency))
		message.WriteString("Gross hourly estimate by eligible location:\n")
		message.WriteString("This estimate may omit locations not currently available; those locations remain eligible if allowed above.\n")
		for _, location := range notice.Estimate.Locations {
			message.WriteString(fmt.Sprintf("- %s: %s %s\n", location.Location, location.HourlyGross, notice.Estimate.Currency))
		}
	} else {
		message.WriteString("Public gross hourly pricing estimate: unavailable.\n")
		message.WriteString("Pricing is informational only; no price cap or location ranking is applied.\n")
	}
	message.WriteString("One central provider creation request will occur after approval.\n")
	message.WriteString("To approve exactly these terms, reply exactly:\n")
	message.WriteString(ApprovalCommandPrefix)
	message.WriteString(notice.Code)
	if message.Len() > approvalMessageLimit {
		return "", errors.New("migration approval notice exceeds the limit")
	}
	return message.String(), nil
}

func newApprovalCode() (string, error) {
	var random [approvalCodeBytes]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return approvalCodePrefix + hex.EncodeToString(random[:]), nil
}

func validApprovalCode(code string) bool {
	if !strings.HasPrefix(code, approvalCodePrefix) || len(code) != len(approvalCodePrefix)+approvalCodeBytes*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(code, approvalCodePrefix))
	return err == nil
}

func samePriceRequest(left, right PriceRequest) bool {
	if left.ServerType != right.ServerType || len(left.Locations) != len(right.Locations) {
		return false
	}
	for index := range left.Locations {
		if left.Locations[index] != right.Locations[index] {
			return false
		}
	}
	return true
}

func validApprovalEstimate(request PriceRequest, estimate PriceEstimate) bool {
	if validateMachineRequest(request.ServerType, request.Locations) != nil || estimate.ServerType != request.ServerType {
		return false
	}
	if estimate.Status == "unavailable" {
		return len(estimate.Locations) == 0 && (estimate.Currency == "" || safeCurrency(estimate.Currency))
	}
	if estimate.Status != "quoted" || !safeCurrency(estimate.Currency) || len(estimate.Locations) == 0 || len(estimate.Locations) > maxMigrationLocations {
		return false
	}
	allowed := make(map[string]struct{}, len(request.Locations))
	for _, location := range request.Locations {
		allowed[location] = struct{}{}
	}
	seen := make(map[string]struct{}, len(estimate.Locations))
	for _, location := range estimate.Locations {
		if !safeMachineName(location.Location, 128) || !validEstimatePrice(location.HourlyGross, estimate.Currency) {
			return false
		}
		if len(request.Locations) > 0 {
			if _, ok := allowed[location.Location]; !ok {
				return false
			}
		}
		if _, ok := seen[location.Location]; ok {
			return false
		}
		seen[location.Location] = struct{}{}
	}
	return true
}

func validEstimatePrice(value, currency string) bool {
	_, err := parseDecimal(value)
	return err == nil && safeCurrency(currency)
}

func unavailablePriceEstimate(request PriceRequest) PriceEstimate {
	return PriceEstimate{Status: "unavailable", ServerType: request.ServerType}
}

func normalizeApprovalEstimate(request PriceRequest, estimate PriceEstimate) PriceEstimate {
	if estimate.Status == "unavailable" {
		return unavailablePriceEstimate(request)
	}
	estimate.LowestHourlyGross = ""
	estimate.Locations = append([]PriceLocation(nil), estimate.Locations...)
	return estimate
}
