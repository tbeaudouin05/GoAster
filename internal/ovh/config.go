// Package ovh implements an explicitly confirmed, local OVH onboarding workflow.
package ovh

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const Repository = "https://github.com/tbeaudouin05/GoAster.git"

var endpoints = map[string]string{
	"ovh-eu": "https://eu.api.ovh.com/1.0",
	"ovh-ca": "https://ca.api.ovh.com/1.0",
	"ovh-us": "https://api.us.ovhcloud.com/1.0",
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var commitID = regexp.MustCompile(`^[0-9a-f]{40}$`)
var sshUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// Config contains validated local settings. Credentials are private to this package.
type Config struct {
	Endpoint, ProjectID, Region, ImageID, FlavorID, SSHKeyID, ServerName string
	SSHUser, SSHPrivateKey, Ref                                          string
	applicationKey, applicationSecret, consumerKey                       string
}

// ConfigFromValues validates settings already loaded by localenv.Store. It never
// includes entered values in errors.
func ConfigFromValues(values map[string]string) (Config, error) {
	required := []string{"OVH_ENDPOINT", "OVH_APPLICATION_KEY", "OVH_APPLICATION_SECRET", "OVH_CONSUMER_KEY", "OVH_PROJECT_ID", "OVH_REGION", "OVH_IMAGE_ID", "OVH_FLAVOR_ID", "OVH_SSH_KEY_ID", "OVH_SERVER_NAME", "OVH_SSH_USER", "OVH_SSH_PRIVATE_KEY", "OVH_GOASTER_REF"}
	for _, name := range required {
		v := values[name]
		if strings.TrimSpace(v) == "" || strings.TrimSpace(v) != v || strings.HasPrefix(v, "replace-with-") || strings.ContainsAny(v, "\r\n\x00") {
			return Config{}, fmt.Errorf("%s must be set to a non-placeholder value without surrounding whitespace", name)
		}
	}
	c := Config{Endpoint: values["OVH_ENDPOINT"], applicationKey: values["OVH_APPLICATION_KEY"], applicationSecret: values["OVH_APPLICATION_SECRET"], consumerKey: values["OVH_CONSUMER_KEY"], ProjectID: values["OVH_PROJECT_ID"], Region: values["OVH_REGION"], ImageID: values["OVH_IMAGE_ID"], FlavorID: values["OVH_FLAVOR_ID"], SSHKeyID: values["OVH_SSH_KEY_ID"], ServerName: values["OVH_SERVER_NAME"], SSHUser: values["OVH_SSH_USER"], SSHPrivateKey: values["OVH_SSH_PRIVATE_KEY"], Ref: values["OVH_GOASTER_REF"]}
	if _, ok := endpoints[c.Endpoint]; !ok {
		return Config{}, fmt.Errorf("OVH_ENDPOINT must be ovh-eu, ovh-ca, or ovh-us")
	}
	for _, item := range []struct{ name, value string }{{"OVH_PROJECT_ID", c.ProjectID}, {"OVH_REGION", c.Region}, {"OVH_IMAGE_ID", c.ImageID}, {"OVH_FLAVOR_ID", c.FlavorID}, {"OVH_SSH_KEY_ID", c.SSHKeyID}, {"OVH_SERVER_NAME", c.ServerName}} {
		if !identifier.MatchString(item.value) {
			return Config{}, fmt.Errorf("%s has an invalid format", item.name)
		}
	}
	if !sshUser.MatchString(c.SSHUser) {
		return Config{}, fmt.Errorf("OVH_SSH_USER has an invalid format")
	}
	if !filepath.IsAbs(c.SSHPrivateKey) {
		return Config{}, fmt.Errorf("OVH_SSH_PRIVATE_KEY must be an absolute local path")
	}
	if !commitID.MatchString(c.Ref) {
		return Config{}, fmt.Errorf("OVH_GOASTER_REF must be a full lowercase 40-character commit ID")
	}
	return c, nil
}

// String and GoString prevent accidental formatting of the credential fields.
func (Config) String() string   { return "OVH configuration (credentials redacted)" }
func (Config) GoString() string { return "OVH configuration (credentials redacted)" }
