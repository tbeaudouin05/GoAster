// Package ovh implements an explicitly confirmed, local OVH onboarding workflow.
package ovh

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const Repository = "https://github.com/tbeaudouin05/GoAster.git"

const (
	// DefaultSSHUser is created by the Ubuntu cloud-init user data and is the
	// only account used by the automated provisioning workflow.
	DefaultSSHUser    = "goaster"
	defaultSSHKeyName = "goaster"
)

var endpoints = map[string]string{
	"ovh-eu": "https://eu.api.ovh.com/1.0",
	"ovh-ca": "https://ca.api.ovh.com/1.0",
	"ovh-us": "https://api.us.ovhcloud.com/1.0",
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var commitID = regexp.MustCompile(`^[0-9a-f]{40}$`)
var sshUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// Config contains validated local settings. Credentials and the local private
// key are never included in its formatted representation.
//
// The selection fields remain available for package-level callers that already
// construct an explicit plan. The /ovh command clears them and discovers fresh
// values from OVH instead of trusting old environment entries.
type Config struct {
	Endpoint, ProjectID, Region, ImageID, FlavorID, SSHKeyID, ServerName string
	SSHUser, SSHPrivateKey, Ref                                          string
	applicationKey, applicationSecret, consumerKey                       string
	publicKey, userData, sshKeyName                                      string
}

// ConfigFromValues validates settings already loaded by localenv.Store. Only
// OVH API credentials are required. The remaining names are accepted as
// legacy optional values so older callers can migrate without losing access;
// automatic provisioning deliberately ignores them.
func ConfigFromValues(values map[string]string) (Config, error) {
	required := []string{"OVH_ENDPOINT", "OVH_APPLICATION_KEY", "OVH_APPLICATION_SECRET", "OVH_CONSUMER_KEY"}
	for _, name := range required {
		if err := validateValue(name, values[name], true); err != nil {
			return Config{}, err
		}
	}

	c := Config{
		Endpoint:          values["OVH_ENDPOINT"],
		applicationKey:    values["OVH_APPLICATION_KEY"],
		applicationSecret: values["OVH_APPLICATION_SECRET"],
		consumerKey:       values["OVH_CONSUMER_KEY"],
	}
	if _, ok := endpoints[c.Endpoint]; !ok {
		return Config{}, fmt.Errorf("OVH_ENDPOINT must be ovh-eu, ovh-ca, or ovh-us")
	}

	for _, item := range []struct {
		name   string
		target *string
	}{
		{"OVH_PROJECT_ID", &c.ProjectID},
		{"OVH_REGION", &c.Region},
		{"OVH_IMAGE_ID", &c.ImageID},
		{"OVH_FLAVOR_ID", &c.FlavorID},
		{"OVH_SSH_KEY_ID", &c.SSHKeyID},
		{"OVH_SERVER_NAME", &c.ServerName},
	} {
		value := values[item.name]
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := validateValue(item.name, value, false); err != nil {
			return Config{}, err
		}
		if !identifier.MatchString(value) {
			return Config{}, fmt.Errorf("%s has an invalid format", item.name)
		}
		*item.target = value
	}

	if value := values["OVH_SSH_USER"]; strings.TrimSpace(value) != "" {
		if err := validateValue("OVH_SSH_USER", value, false); err != nil {
			return Config{}, err
		}
		if !sshUserPattern.MatchString(value) {
			return Config{}, fmt.Errorf("OVH_SSH_USER has an invalid format")
		}
		c.SSHUser = value
	}
	if value := values["OVH_SSH_PRIVATE_KEY"]; strings.TrimSpace(value) != "" {
		if err := validateValue("OVH_SSH_PRIVATE_KEY", value, false); err != nil {
			return Config{}, err
		}
		if !filepath.IsAbs(value) {
			return Config{}, fmt.Errorf("OVH_SSH_PRIVATE_KEY must be an absolute local path")
		}
		c.SSHPrivateKey = value
	}
	if value := values["OVH_GOASTER_REF"]; strings.TrimSpace(value) != "" {
		if err := validateValue("OVH_GOASTER_REF", value, false); err != nil {
			return Config{}, err
		}
		if !commitID.MatchString(value) {
			return Config{}, fmt.Errorf("OVH_GOASTER_REF must be a full lowercase 40-character commit ID")
		}
		c.Ref = value
	}
	return c, nil
}

func validateValue(name, value string, required bool) error {
	if strings.TrimSpace(value) == "" {
		if required {
			return fmt.Errorf("%s must be set to a non-placeholder value without surrounding whitespace", name)
		}
		return nil
	}
	if strings.TrimSpace(value) != value || strings.HasPrefix(value, "replace-with-") || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s must be set to a non-placeholder value without surrounding whitespace", name)
	}
	return nil
}

// WithAutomaticProvisioning supplies local-only values and clears all legacy
// selection overrides. The public key is used both for the OVH project key
// and for the fixed goaster login created by cloud-init.
func (c Config) WithAutomaticProvisioning(pair KeyPair, ref string) (Config, error) {
	if err := pair.validate(); err != nil {
		return Config{}, fmt.Errorf("invalid local GoAster key: %w", err)
	}
	publicKey, _ := normalizePublicKey(pair.PublicKey)
	pair.PublicKey = publicKey
	if !commitID.MatchString(ref) {
		return Config{}, fmt.Errorf("GoAster commit is not a full lowercase 40-character commit ID")
	}
	c.ProjectID = ""
	c.Region = ""
	c.ImageID = ""
	c.FlavorID = ""
	c.SSHKeyID = ""
	c.ServerName = ""
	c.SSHUser = DefaultSSHUser
	c.SSHPrivateKey = pair.PrivateKeyPath
	c.Ref = ref
	c.publicKey = pair.PublicKey
	c.sshKeyName = defaultSSHKeyName
	c.userData = cloudInitUserData(pair.PublicKey)
	return c, nil
}

func cloudInitUserData(publicKey string) string {
	return "#cloud-config\n" +
		"users:\n" +
		"  - default\n" +
		"  - name: " + DefaultSSHUser + "\n" +
		"    gecos: GoAster\n" +
		"    home: /var/lib/goaster\n" +
		"    groups: [sudo]\n" +
		"    shell: /bin/bash\n" +
		"    lock_passwd: true\n" +
		"    sudo: [\"ALL=(ALL) NOPASSWD:ALL\"]\n" +
		"    ssh_authorized_keys:\n" +
		"      - " + publicKey + "\n"
}

// NewServerName returns a short, collision-resistant name that is safe for
// the OVH instance API. It does not include user or repository data.
func NewServerName() (string, error) {
	randomPart := make([]byte, 4)
	if _, err := rand.Read(randomPart); err != nil {
		return "", fmt.Errorf("generate server name: %w", err)
	}
	name := fmt.Sprintf("goaster-%s-%s", time.Now().UTC().Format("20060102-150405"), hex.EncodeToString(randomPart))
	if !identifier.MatchString(name) {
		return "", fmt.Errorf("generated server name is invalid")
	}
	return name, nil
}

// String and GoString prevent accidental formatting of credential fields.
func (Config) String() string   { return "OVH configuration (credentials redacted)" }
func (Config) GoString() string { return "OVH configuration (credentials redacted)" }
