package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"goaster/internal/localenv"
	"goaster/internal/ovh"
	"goaster/internal/telegram"
)

// runOVHSetup is local-only: no settings are passed to the chat provider.
func runOVHSetup(ctx context.Context, scanner *bufio.Scanner, out *bufio.Writer, store *localenv.Store, picker EnvironmentFilePicker, auth CredentialStore) error {
	ssh := ovh.OpenSSH{}
	return runOVHSetupWithOptionsAndPicker(ctx, scanner, out, store, ovh.DefaultHTTPClient(), ssh, ssh, ovh.EnsureLocalKeyPair, ovh.ResolveGoasterCommit, picker, auth)
}

func runOVHSetupWithDependencies(ctx context.Context, scanner *bufio.Scanner, out *bufio.Writer, store *localenv.Store, httpClient ovh.HTTPClient, ssh ovh.SSHRunner, remote ovh.RemoteRunner) error {
	return runOVHSetupWithDependenciesAndOptions(ctx, scanner, out, store, httpClient, ssh, remote, ovh.EnsureLocalKeyPair, ovh.ResolveGoasterCommit)
}

func runOVHSetupWithDependenciesAndOptions(ctx context.Context, scanner *bufio.Scanner, out *bufio.Writer, store *localenv.Store, httpClient ovh.HTTPClient, ssh ovh.SSHRunner, remote ovh.RemoteRunner, keyPair func() (ovh.KeyPair, error), commit func() (string, error)) error {
	return runOVHSetupWithOptionsAndPicker(ctx, scanner, out, store, httpClient, ssh, remote, keyPair, commit, nativeEnvironmentFilePicker{})
}

func runOVHSetupWithOptionsAndPicker(ctx context.Context, scanner *bufio.Scanner, out *bufio.Writer, store *localenv.Store, httpClient ovh.HTTPClient, ssh ovh.SSHRunner, remote ovh.RemoteRunner, keyPair func() (ovh.KeyPair, error), commit func() (string, error), picker EnvironmentFilePicker, authStores ...CredentialStore) error {
	if store == nil {
		fmt.Fprintln(out, "OVH configuration rejected: environment settings store is unavailable.")
		return out.Flush()
	}
	config, err := ovh.ConfigFromValues(store.Values())
	if err != nil {
		fmt.Fprintf(out, "OVH configuration rejected: %v. Run /env first to select and save valid OVH settings locally.\n", err)
		return out.Flush()
	}
	var authStore CredentialStore
	if len(authStores) > 0 {
		authStore = authStores[0]
	} else {
		authStore, err = NewCredentialStore("")
	}
	credential, loadErr := authStore.Load()
	if err != nil || loadErr != nil {
		fmt.Fprintln(out, "Runtime subscription credential unavailable; sign in locally before provisioning.")
		return out.Flush()
	}
	authJSON, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("runtime subscription credential could not be prepared")
	}
	bundle, err := ovh.NewRuntimeBundle(authJSON, store.Values())
	if err != nil {
		fmt.Fprintln(out, "Runtime credentials rejected; correct the local settings before provisioning.")
		return out.Flush()
	}
	pair, err := keyPair()
	if err != nil {
		fmt.Fprintf(out, "OVH local SSH key unavailable: %v\n", err)
		return out.Flush()
	}
	ref, err := commit()
	if err != nil {
		fmt.Fprintf(out, "GoAster commit unavailable: %v\n", err)
		return out.Flush()
	}
	config, err = config.WithAutomaticProvisioning(pair, ref)
	if err != nil {
		fmt.Fprintf(out, "OVH configuration rejected: %v\n", err)
		return out.Flush()
	}
	client, err := ovh.NewClient(config, httpClient)
	if err != nil {
		fmt.Fprintln(out, "OVH client configuration rejected.")
		return out.Flush()
	}
	workflow := ovh.Workflow{
		Client: client, SSH: ssh, Remote: remote, Runtime: bundle,
		Select: func(ctx context.Context, kind string, choices []ovh.Choice) (string, error) {
			return chooseOVHOption(ctx, scanner, out, kind, choices)
		},
		Confirm: func(ctx context.Context, p ovh.Plan) (bool, error) {
			keyID := p.SSHKeyID
			if keyID == "" {
				keyID = "will be uploaded after confirmation"
			}
			fmt.Fprintf(out, "OVH preflight passed. Review this billable, public server:\n  Project: %s\n  Region: %s\n  Server name: %s\n  Image: %q (%s)\n  Flavor: %q (%s)\n  SSH key: %q (%s)\n  SSH user: %s\n  Repository: %s\n  Commit: %s\n  Billing: hourly; public network\n", p.ProjectID, p.Region, p.ServerName, p.ImageName, p.ImageID, p.FlavorName, p.FlavorID, p.SSHKeyName, keyID, p.SSHUser, ovh.Repository, p.Ref)
			fmt.Fprintln(out, "The persistent local private key stays local; only its public key is sent to OVH.")
			fmt.Fprintln(out, "After creation GoAster will connect over SSH, install packages, clone the pinned repository, transfer the subscription and saved OVH/Telegram runtime credentials through encrypted SSH stdin, and enable the agent service.")
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
	fmt.Fprintln(out, "The service is running with the transferred subscription credential.")
	return offerTelegramSetup(ctx, scanner, out, store, picker, remote, ovh.Target{IP: result.IP, User: result.Plan.SSHUser, PrivateKey: pair.PrivateKeyPath})
}

type telegramSetupRunner interface {
	SetupTelegram(context.Context, ovh.Target, string, string) error
}

// The target exists only while this post-provisioning prompt is running.
func offerTelegramSetup(ctx context.Context, scanner *bufio.Scanner, out *bufio.Writer, store *localenv.Store, picker EnvironmentFilePicker, remote ovh.RemoteRunner, target ovh.Target) error {
	fmt.Fprintln(out, "Optional Telegram setup confirms the bot token and starts a private, outbound-only adapter; the initial runtime bundle already includes any saved token.")
	fmt.Fprintln(out, "Create a bot with @BotFather and import its token with the OVH settings using /env. The target is retained only during this workflow.")
	var code, hash string
	for {
		fmt.Fprint(out, "Type SETUP TELEGRAM to confirm, /env to import settings, or anything else to skip: ")
		if err := out.Flush(); err != nil {
			return err
		}
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("read Telegram setup confirmation: %w", err)
			}
			fmt.Fprintln(out, "\nTelegram setup skipped.")
			return out.Flush()
		}
		answer := strings.TrimSpace(scanner.Text())
		if answer == "/env" {
			if err := runEnvironmentFileSetup(scanner, out, store, picker, ""); err != nil {
				return err
			}
			fmt.Fprintln(out, "Back in Telegram setup for the server just provisioned.")
			continue
		}
		if answer != "SETUP TELEGRAM" {
			fmt.Fprintln(out, "Telegram setup skipped.")
			return out.Flush()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		token := store.Values()["TELEGRAM_BOT_TOKEN"]
		if err := telegram.ValidateToken(token); err != nil {
			fmt.Fprintln(out, "Telegram token is missing or invalid. Use /env with the token and all OVH variables; the import replaces the saved set.")
			continue
		}
		runner, ok := remote.(telegramSetupRunner)
		if !ok {
			fmt.Fprintln(out, "Telegram setup is unavailable for this SSH runner.")
			return out.Flush()
		}
		if code == "" {
			var err error
			code, hash, err = telegram.PairingCode()
			if err != nil {
				fmt.Fprintln(out, "Telegram pairing code could not be generated.")
				return out.Flush()
			}
		}
		if err := runner.SetupTelegram(ctx, target, token, hash); err != nil {
			// SSH errors can contain command output or URLs; never print them.
			fmt.Fprintln(out, "Telegram setup or service verification failed; remote setup may be incomplete. Check the bot token, outbound access and webhook settings, then retry or skip.")
			continue
		}
		fmt.Fprintln(out, "Telegram service is active and polling. Pair exactly one Telegram account in a private chat with your bot:")
		fmt.Fprintf(out, "  /start %s\n", code)
		fmt.Fprintln(out, "Keep this one-time code private. Chat uses the transferred subscription credential.")
		return out.Flush()
	}
}

func chooseOVHOption(ctx context.Context, scanner *bufio.Scanner, out *bufio.Writer, kind string, choices []ovh.Choice) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fmt.Fprintf(out, "Multiple %s are available; choose one:\n", kind)
	for index, choice := range choices {
		name := choice.Name
		if name == "" {
			name = choice.ID
		}
		if choice.Detail == "" {
			fmt.Fprintf(out, "  %d) %s (%s)\n", index+1, name, choice.ID)
		} else {
			fmt.Fprintf(out, "  %d) %s (%s; %s)\n", index+1, name, choice.ID, choice.Detail)
		}
	}
	fmt.Fprintf(out, "Select %s [1-%d]: ", kind, len(choices))
	if err := out.Flush(); err != nil {
		return "", err
	}
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("input ended while choosing %s", kind)
	}
	answer := strings.TrimSpace(scanner.Text())
	for _, choice := range choices {
		if answer == choice.ID {
			return choice.ID, nil
		}
	}
	index, err := strconv.Atoi(answer)
	if err != nil || index < 1 || index > len(choices) {
		return "", fmt.Errorf("invalid choice for %s", kind)
	}
	return choices[index-1].ID, nil
}
