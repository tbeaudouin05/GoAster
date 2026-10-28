package ovh

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

var ErrRefused = errors.New("server creation was not confirmed")

// Plan is the non-secret proposal shown to the person approving creation.
type Plan struct {
	ProjectID, Region, ImageID, ImageName, FlavorID, FlavorName string
	SSHKeyID, SSHKeyName, ServerName, SSHUser, Ref              string
}

type Target struct{ IP, User, PrivateKey string }

// SSHRunner tests whether the instance accepts a noninteractive SSH login.
type SSHRunner interface {
	Check(context.Context, Target) error
}

// RemoteRunner installs and verifies the service on the confirmed instance.
type RemoteRunner interface {
	Install(context.Context, Target, string) (commit, serviceStatus string, err error)
}

type Result struct {
	Plan                                Plan
	CreationAttempted                   bool
	ServerID, IP, Commit, ServiceStatus string
}

type Workflow struct {
	Client                *Client
	SSH                   SSHRunner
	Remote                RemoteRunner
	Confirm               func(context.Context, Plan) (bool, error)
	Created               func(Result) error
	PollInterval, Timeout time.Duration
}

// Run performs preflight before invoking Confirm. It never deletes a server.
// The partial Result survives every failure, including cancellation. If a
// create request fails without an ID, creation may still have succeeded.
func (w Workflow) Run(ctx context.Context) (Result, error) {
	var result Result
	if w.Client == nil || w.SSH == nil || w.Remote == nil || w.Confirm == nil {
		return result, fmt.Errorf("OVH workflow dependencies and confirmation callback are required")
	}
	plan, err := w.Client.Preflight(ctx)
	if err != nil {
		return result, fmt.Errorf("preflight: %w", err)
	}
	result.Plan = plan
	confirmed, err := w.Confirm(ctx, plan)
	if err != nil {
		return result, fmt.Errorf("server confirmation failed")
	}
	if !confirmed {
		return result, ErrRefused
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	interval := w.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	result.CreationAttempted = true
	server, err := w.Client.create(ctx)
	if err != nil {
		return result, fmt.Errorf("create server: %w; creation outcome may be unknown, check the OVH project before retrying", err)
	}
	result.ServerID = server.ID
	if w.Created != nil {
		if err := w.Created(result); err != nil {
			return result, fmt.Errorf("could not report the created server")
		}
	}
	for {
		server, err = w.Client.server(ctx, result.ServerID)
		if err != nil {
			return result, fmt.Errorf("get server status: %w", err)
		}
		if server.ID != result.ServerID {
			return result, fmt.Errorf("server status response did not match the created server")
		}
		if strings.EqualFold(server.Status, "ERROR") {
			return result, fmt.Errorf("server entered ERROR status")
		}
		if strings.EqualFold(server.Status, "ACTIVE") {
			result.IP = publicIP(server.IPAddresses)
			if result.IP != "" {
				break
			}
		}
		if err := pause(ctx, interval); err != nil {
			return result, fmt.Errorf("wait for active server and public IP: %w", err)
		}
	}
	target := Target{IP: result.IP, User: w.Client.config.SSHUser, PrivateKey: w.Client.config.SSHPrivateKey}
	for {
		if err := w.SSH.Check(ctx, target); err == nil {
			break
		}
		if err := pause(ctx, interval); err != nil {
			return result, fmt.Errorf("wait for SSH readiness: %w", err)
		}
	}
	commit, status, err := w.Remote.Install(ctx, target, plan.Ref)
	if err != nil {
		return result, fmt.Errorf("install or verify agent service failed")
	}
	// Do not report untrusted remote output as a successful verification.
	if commit != plan.Ref || status != "active" {
		return result, fmt.Errorf("agent commit or service status did not match the expected result")
	}
	result.Commit, result.ServiceStatus = commit, status
	return result, nil
}

// OVH may include a CIDR suffix. Prefer IPv4 when both families are available.
func publicIP(addresses []ipAddress) string {
	var ipv6 string
	for _, address := range addresses {
		if !strings.EqualFold(address.Type, "public") {
			continue
		}
		ip, err := netip.ParseAddr(address.IP)
		if err != nil {
			prefix, prefixErr := netip.ParsePrefix(address.IP)
			if prefixErr != nil {
				continue
			}
			ip = prefix.Addr()
		}
		ip = ip.Unmap()
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			continue
		}
		if ip.Is4() {
			return ip.String()
		}
		if ipv6 == "" {
			ipv6 = ip.String()
		}
	}
	return ipv6
}

func pause(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
