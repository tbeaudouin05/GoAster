package migration

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"goaster/internal/diagnostics"
)

func testApprovalEstimate() PriceEstimate {
	return PriceEstimate{
		Status:            "quoted",
		ServerType:        "cx22",
		Currency:          "EUR",
		LowestHourlyGross: "0.10",
		Locations: []PriceLocation{
			{Location: "fsn1", HourlyGross: "0.10"},
			{Location: "nbg1", HourlyGross: "0.11"},
		},
	}
}

func TestApprovalMessageExplainsPartialEstimate(t *testing.T) {
	estimate := testApprovalEstimate()
	estimate.Locations = estimate.Locations[:1]
	message, err := ApprovalMessage(ApprovalNotice{
		Code:     approvalCodePrefix + strings.Repeat("a", approvalCodeBytes*2),
		Request:  PriceRequest{ServerType: "cx22", Locations: []string{"fsn1", "nbg1"}},
		Estimate: estimate,
	})
	if err != nil || !strings.Contains(message, "Strict locations: fsn1, nbg1") || !strings.Contains(message, "may omit locations not currently available; those locations remain eligible") {
		t.Fatalf("partial estimate notice = %q, %v", message, err)
	}
}

func TestApprovalControllerBindsPrivateOwnerApprovalAndConsumesIt(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	subject := ApprovalSubject{UserID: 42, ChatID: 42, Private: true}
	request := Request{ServerType: "cx22", Locations: []string{"fsn1", "nbg1"}}
	notices := make([]ApprovalNotice, 0, 1)
	executed := make([]Request, 0, 1)
	controller := NewApprovalController(
		func() time.Time { return now },
		func(context.Context, PriceRequest) (PriceEstimate, error) { return testApprovalEstimate(), nil },
		func(_ context.Context, notice ApprovalNotice) error {
			notices = append(notices, notice)
			return nil
		},
		func(_ context.Context, approved Request) (Result, error) {
			executed = append(executed, approved)
			return Result{Status: "created"}, nil
		},
	)
	privateContext := WithApprovalSubject(context.Background(), subject)
	result, err := controller.Start(privateContext, request)
	if err != nil || result.Status != "approval_required" || len(notices) != 1 || len(executed) != 0 {
		t.Fatalf("initial approval request = %#v, %v; notices=%d executed=%d", result, err, len(notices), len(executed))
	}
	message, err := ApprovalMessage(notices[0])
	if err != nil || !strings.Contains(message, "Server type: cx22") || !strings.Contains(message, "Strict locations: fsn1, nbg1") || !strings.Contains(message, "Gross hourly estimate by eligible location:") || !strings.Contains(message, "may omit locations not currently available; those locations remain eligible") || strings.Contains(message, "Lowest gross hourly estimate") || !strings.Contains(message, ApprovalCommandPrefix+notices[0].Code) {
		t.Fatalf("approval notice = %q, %v", message, err)
	}
	if _, ok := ApprovalCode(ApprovalCommandPrefix + notices[0].Code); !ok {
		t.Fatal("source approval phrase was not recognized")
	}
	if _, ok := ApprovalCode(" " + ApprovalCommandPrefix + notices[0].Code); ok {
		t.Fatal("approval phrase accepted leading whitespace")
	}

	wrongSubject, err := controller.Approve(context.Background(), ApprovalCommand{Code: notices[0].Code, Subject: ApprovalSubject{UserID: 43, ChatID: 43, Private: true}})
	if err != nil || wrongSubject.Status != "rejected" {
		t.Fatalf("wrong-subject approval = %#v, %v", wrongSubject, err)
	}
	group, err := controller.Approve(context.Background(), ApprovalCommand{Code: notices[0].Code, Subject: ApprovalSubject{UserID: 42, ChatID: -10042, Private: false}})
	if err != nil || group.Status != "rejected" {
		t.Fatalf("group approval = %#v, %v", group, err)
	}
	approved, err := controller.Approve(context.Background(), ApprovalCommand{Code: notices[0].Code, Subject: subject})
	if err != nil || approved.Status != "approved" {
		t.Fatalf("private approval = %#v, %v", approved, err)
	}
	replayedApproval, err := controller.Approve(context.Background(), ApprovalCommand{Code: notices[0].Code, Subject: subject})
	if err != nil || replayedApproval.Status != "rejected" {
		t.Fatalf("replayed approval = %#v, %v", replayedApproval, err)
	}

	result, err = controller.Start(privateContext, request)
	if err != nil || result.Status != "created" || len(executed) != 1 {
		t.Fatalf("approved start = %#v, %v; executed=%d", result, err, len(executed))
	}
	if executed[0].ApprovedHourlyGross != "" || executed[0].ApprovedCurrency != "" || executed[0].JobID != "" || !samePriceRequest(PriceRequest{ServerType: executed[0].ServerType, Locations: executed[0].Locations}, PriceRequest{ServerType: request.ServerType, Locations: request.Locations}) {
		t.Fatalf("executed terms unexpectedly depended on informational pricing: %#v", executed[0])
	}
	forged, err := controller.Start(privateContext, Request{ServerType: "cx22", Locations: []string{"fsn1", "nbg1"}, ApprovedHourlyGross: "0.01", ApprovedCurrency: "EUR", JobID: "migration-forged"})
	if err != nil || forged.Status != "rejected" || len(executed) != 1 {
		t.Fatalf("forged post-approval start = %#v, %v; executed=%d", forged, err, len(executed))
	}
}

func TestApprovalControllerAllowsUnavailableInformationalEstimate(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	subject := ApprovalSubject{UserID: 42, ChatID: 42, Private: true}
	request := Request{ServerType: "cx22", Locations: []string{"fsn1"}}
	var notice ApprovalNotice
	executed := 0
	controller := NewApprovalController(
		func() time.Time { return now },
		func(context.Context, PriceRequest) (PriceEstimate, error) {
			return PriceEstimate{Status: "unavailable", ServerType: "cx22"}, nil
		},
		func(_ context.Context, delivered ApprovalNotice) error {
			notice = delivered
			return nil
		},
		func(_ context.Context, approved Request) (Result, error) {
			executed++
			if approved.ServerType != request.ServerType || !samePriceRequest(PriceRequest{ServerType: approved.ServerType, Locations: approved.Locations}, PriceRequest{ServerType: request.ServerType, Locations: request.Locations}) {
				t.Fatalf("approved request changed exact target terms: %#v", approved)
			}
			if approved.ApprovedHourlyGross != "" || approved.ApprovedCurrency != "" {
				t.Fatalf("unavailable pricing became an approval constraint: %#v", approved)
			}
			return Result{Status: "created"}, nil
		},
	)
	privateContext := WithApprovalSubject(context.Background(), subject)
	result, err := controller.Start(privateContext, request)
	if err != nil || result.Status != "approval_required" || notice.Estimate.Status != "unavailable" {
		t.Fatalf("unavailable estimate did not produce an informed approval: %#v, %v; notice=%#v", result, err, notice)
	}
	message, err := ApprovalMessage(notice)
	if err != nil || !strings.Contains(message, "pricing estimate: unavailable") || !strings.Contains(message, "no price cap or location ranking") || !strings.Contains(message, "One central provider creation request will occur after approval") {
		t.Fatalf("unavailable approval message = %q, %v", message, err)
	}
	approved, err := controller.Approve(context.Background(), ApprovalCommand{Code: notice.Code, Subject: subject})
	if err != nil || approved.Status != "approved" {
		t.Fatalf("unavailable estimate approval = %#v, %v", approved, err)
	}
	result, err = controller.Start(privateContext, request)
	if err != nil || result.Status != "created" || executed != 1 {
		t.Fatalf("approved unavailable estimate = %#v, %v; executed=%d", result, err, executed)
	}
}

func TestApprovalControllerTreatsEstimateFailureAsUnavailable(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	subject := ApprovalSubject{UserID: 42, ChatID: 42, Private: true}
	var notice ApprovalNotice
	var executed int
	var diagnosticsOutput bytes.Buffer
	restoreDiagnostics := diagnostics.SetOutput(&diagnosticsOutput)
	defer restoreDiagnostics()
	controller := NewApprovalController(
		func() time.Time { return now },
		func(context.Context, PriceRequest) (PriceEstimate, error) {
			return PriceEstimate{}, errors.New("synthetic pricing endpoint unavailable")
		},
		func(_ context.Context, delivered ApprovalNotice) error {
			notice = delivered
			return nil
		},
		func(_ context.Context, _ Request) (Result, error) {
			executed++
			return Result{Status: "created"}, nil
		},
	)
	privateContext := WithApprovalSubject(context.Background(), subject)
	result, err := controller.Start(privateContext, Request{ServerType: "cx22"})
	if err != nil || result.Status != "approval_required" || notice.Estimate.Status != "unavailable" {
		t.Fatalf("estimate failure blocked informed approval: %#v, %v; notice=%#v", result, err, notice)
	}
	message, err := ApprovalMessage(notice)
	if err != nil || !strings.Contains(message, "pricing estimate: unavailable") {
		t.Fatalf("estimate failure approval message = %q, %v", message, err)
	}
	if !strings.Contains(diagnosticsOutput.String(), "ERROR operation=migration.approval.estimate") {
		t.Fatalf("estimate failure was not diagnosed safely: %q", diagnosticsOutput.String())
	}
	approved, err := controller.Approve(context.Background(), ApprovalCommand{Code: notice.Code, Subject: subject})
	if err != nil || approved.Status != "approved" {
		t.Fatalf("estimate failure approval = %#v, %v", approved, err)
	}
	result, err = controller.Start(privateContext, Request{ServerType: "cx22"})
	if err != nil || result.Status != "created" || executed != 1 {
		t.Fatalf("approved estimate failure = %#v, %v; executed=%d", result, err, executed)
	}
}

func TestApprovalControllerRejectsGroupAndExpiresPendingApproval(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	quotes := 0
	deliveries := 0
	codes := make([]string, 0, 2)
	controller := NewApprovalController(
		func() time.Time { return now },
		func(context.Context, PriceRequest) (PriceEstimate, error) {
			quotes++
			return testApprovalEstimate(), nil
		},
		func(_ context.Context, notice ApprovalNotice) error {
			deliveries++
			codes = append(codes, notice.Code)
			return nil
		},
		func(context.Context, Request) (Result, error) { return Result{Status: "created"}, nil },
	)
	groupContext := WithApprovalSubject(context.Background(), ApprovalSubject{UserID: 42, ChatID: -10042, Private: false})
	result, err := controller.Start(groupContext, Request{ServerType: "cx22"})
	if err != nil || result.Status != "approval_required" || quotes != 0 || deliveries != 0 {
		t.Fatalf("group start was not fail-closed: %#v, %v; quotes=%d deliveries=%d", result, err, quotes, deliveries)
	}

	privateSubject := ApprovalSubject{UserID: 42, ChatID: 42, Private: true}
	privateContext := WithApprovalSubject(context.Background(), privateSubject)
	result, err = controller.Start(privateContext, Request{ServerType: "cx22"})
	if err != nil || result.Status != "approval_required" || quotes != 1 || deliveries != 1 {
		t.Fatalf("private approval request = %#v, %v; quotes=%d deliveries=%d", result, err, quotes, deliveries)
	}
	// A second start with the same terms does not issue another notice while
	// the exact first notice is still pending.
	result, err = controller.Start(privateContext, Request{ServerType: "cx22"})
	if err != nil || result.Status != "approval_required" || quotes != 1 || deliveries != 1 {
		t.Fatalf("pending approval was duplicated: %#v, %v; quotes=%d deliveries=%d", result, err, quotes, deliveries)
	}
	now = now.Add(approvalLifetime + time.Second)
	expired, err := controller.Approve(context.Background(), ApprovalCommand{Code: codes[0], Subject: privateSubject})
	if err != nil || expired.Status != "rejected" {
		t.Fatalf("expired approval = %#v, %v", expired, err)
	}
	result, err = controller.Start(privateContext, Request{ServerType: "cx22"})
	if err != nil || result.Status != "approval_required" || quotes != 2 || deliveries != 2 {
		t.Fatalf("expired approval was not replaced: %#v, %v; quotes=%d deliveries=%d", result, err, quotes, deliveries)
	}
}
