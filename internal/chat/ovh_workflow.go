package chat

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"goaster/internal/localenv"
	"goaster/internal/ovh"
)

// runOVHSetup is local-only: no settings are passed to the chat provider.
func runOVHSetup(ctx context.Context, scanner *bufio.Scanner, out *bufio.Writer, store *localenv.Store) error {
	ssh := ovh.OpenSSH{}
	return runOVHSetupWithDependencies(ctx, scanner, out, store, ovh.DefaultHTTPClient(), ssh, ssh)
}

func runOVHSetupWithDependencies(ctx context.Context, scanner *bufio.Scanner, out *bufio.Writer, store *localenv.Store, httpClient ovh.HTTPClient, ssh ovh.SSHRunner, remote ovh.RemoteRunner) error {
	config, err := ovh.ConfigFromValues(store.Values())
	if err != nil {
		fmt.Fprintf(out, "OVH configuration rejected: %v\n", err)
		return out.Flush()
	}
	info, err := os.Lstat(config.SSHPrivateKey)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Mode().Perm()&0400 == 0 || info.Mode().Perm()&0077 != 0 {
		fmt.Fprintln(out, "OVH configuration rejected: SSH private key must be a nonempty, owner-readable local regular file with owner-only permissions.")
		return out.Flush()
	}
	client, err := ovh.NewClient(config, httpClient)
	if err != nil {
		fmt.Fprintln(out, "OVH client configuration rejected.")
		return out.Flush()
	}
	workflow := ovh.Workflow{
		Client: client, SSH: ssh, Remote: remote,
		Confirm: func(ctx context.Context, p ovh.Plan) (bool, error) {
			fmt.Fprintf(out, "OVH preflight passed. Review this billable, public server:\n  Project: %s\n  Region: %s\n  Server name: %s\n  Image: %q (%s)\n  Flavor: %q (%s)\n  SSH key: %q (%s)\n  SSH user: %s\n  Repository: %s\n  Commit: %s\n  Billing: hourly; public network\n", p.ProjectID, p.Region, p.ServerName, p.ImageName, p.ImageID, p.FlavorName, p.FlavorID, p.SSHKeyName, p.SSHKeyID, p.SSHUser, ovh.Repository, p.Ref)
			fmt.Fprintln(out, "After creation GoAster will connect over SSH, install packages, clone the pinned repository, and enable the agent service.")
			fmt.Fprint(out, "Type CREATE to provision this server; anything else cancels: ")
			if err := out.Flush(); err != nil {
				return false, err
			}
			if !scanner.Scan() {
				return false, scanner.Err()
			}
			return strings.TrimSpace(scanner.Text()) == "CREATE", ctx.Err()
		},
		Created: func(r ovh.Result) error {
			fmt.Fprintf(out, "Server created: project=%s region=%s server_id=%s. It will be retained on failure.\n", r.Plan.ProjectID, r.Plan.Region, r.ServerID)
			return out.Flush()
		},
	}
	result, err := workflow.Run(ctx)
	if err != nil {
		fmt.Fprintf(out, "OVH onboarding stopped: %v\n", err)
		if result.ServerID != "" {
			fmt.Fprintf(out, "Retained server: project=%s region=%s server_id=%s public_ip=%s\n", result.Plan.ProjectID, result.Plan.Region, result.ServerID, result.IP)
		}
		return out.Flush()
	}
	fmt.Fprintf(out, "OVH onboarding complete: project=%s region=%s image_id=%s flavor_id=%s ssh_key_id=%s server_id=%s public_ip=%s commit=%s service=%s\n", result.Plan.ProjectID, result.Plan.Region, result.Plan.ImageID, result.Plan.FlavorID, result.Plan.SSHKeyID, result.ServerID, result.IP, result.Commit, result.ServiceStatus)
	fmt.Fprintln(out, "The service is running; authenticate the remote GoAster account separately before using chat.")
	return out.Flush()
}
