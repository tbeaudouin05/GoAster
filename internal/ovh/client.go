package ovh

import (
	"bytes"
	"context"
	"crypto/sha1" // OVH API v1 specifies SHA-1 for request signatures.
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// HTTPClient permits an in-memory fake; production uses an HTTP client that
// refuses redirects so authentication headers cannot be forwarded elsewhere.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type Client struct {
	config      Config
	http        HTTPClient
	clockOffset time.Duration
}

func NewClient(config Config, client HTTPClient) (*Client, error) {
	if _, ok := endpoints[config.Endpoint]; !ok || config.applicationKey == "" || config.applicationSecret == "" || config.consumerKey == "" || client == nil {
		return nil, fmt.Errorf("invalid OVH client configuration")
	}
	return &Client{config: config, http: client}, nil
}

// DefaultHTTPClient never follows redirects and has a bounded request duration.
func DefaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (c *Client) syncTime(ctx context.Context) error {
	var serverTime int64
	before := time.Now()
	if err := c.call(ctx, http.MethodGet, "/auth/time", nil, &serverTime, false); err != nil {
		return fmt.Errorf("get OVH server time: %w", err)
	}
	if serverTime <= 0 {
		return fmt.Errorf("invalid OVH server time")
	}
	c.clockOffset = time.Unix(serverTime, 0).Sub(before.Add(time.Since(before) / 2))
	return nil
}

// call intentionally suppresses transport errors and response bodies, which
// may contain credentials or user data. Only status codes are reported.
func (c *Client) call(ctx context.Context, method, path string, input, output any, authenticated bool) error {
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode OVH request")
		}
	}
	target := endpoints[c.config.Endpoint] + path
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("construct OVH request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ovh-Application", c.config.applicationKey)
	if authenticated {
		timestamp := strconv.FormatInt(time.Now().Add(c.clockOffset).Unix(), 10)
		signature := sha1.Sum([]byte(strings.Join([]string{c.config.applicationSecret, c.config.consumerKey, method, target, string(body), timestamp}, "+")))
		req.Header.Set("X-Ovh-Consumer", c.config.consumerKey)
		req.Header.Set("X-Ovh-Timestamp", timestamp)
		req.Header.Set("X-Ovh-Signature", fmt.Sprintf("$1$%x", signature))
	}
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("OVH request failed")
	}
	if response == nil || response.Body == nil {
		return fmt.Errorf("invalid OVH response")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("OVH API returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil || len(data) > 4*1024*1024 {
		return fmt.Errorf("invalid OVH response body")
	}
	if output != nil && json.Unmarshal(data, output) != nil {
		return fmt.Errorf("invalid OVH response JSON")
	}
	return nil
}

type image struct {
	ID, Region, Name, Status, User, FlavorType string
	MinDisk, MinRam                            int
}
type flavor struct {
	ID, Region, Name, Type  string
	Available               bool
	Disk, Ram, Vcpus, Quota int
}
type sshKey struct {
	ID, Name string
	Regions  []string
}
type ipAddress struct {
	IP, Type string
	Version  int
}
type instance struct {
	ID, Status  string
	IPAddresses []ipAddress `json:"ipAddresses"`
}

func (c *Client) projectPath() string { return "/cloud/project/" + url.PathEscape(c.config.ProjectID) }

// Preflight checks access and verifies the selected items in the chosen region.
func (c *Client) Preflight(ctx context.Context) (Plan, error) {
	if err := c.syncTime(ctx); err != nil {
		return Plan{}, err
	}
	base := c.projectPath()
	var project struct {
		ProjectID string `json:"project_id"`
		Status    string
	}
	if err := c.call(ctx, "GET", base, nil, &project, true); err != nil {
		return Plan{}, fmt.Errorf("check project: %w", err)
	}
	if project.ProjectID != c.config.ProjectID || !strings.EqualFold(project.Status, "ok") {
		return Plan{}, fmt.Errorf("selected project is unavailable")
	}
	var regions []string
	if err := c.call(ctx, "GET", base+"/region", nil, &regions, true); err != nil {
		return Plan{}, fmt.Errorf("check regions: %w", err)
	}
	if !contains(regions, c.config.Region) {
		return Plan{}, fmt.Errorf("selected region is not enabled for the project")
	}
	var region struct{ Name, Status string }
	if err := c.call(ctx, "GET", base+"/region/"+url.PathEscape(c.config.Region), nil, &region, true); err != nil {
		return Plan{}, fmt.Errorf("check region status: %w", err)
	}
	if region.Name != c.config.Region || !strings.EqualFold(region.Status, "UP") {
		return Plan{}, fmt.Errorf("selected region is unavailable")
	}
	query := "?region=" + url.QueryEscape(c.config.Region)
	var images []image
	if err := c.call(ctx, "GET", base+"/image"+query, nil, &images, true); err != nil {
		return Plan{}, fmt.Errorf("check images: %w", err)
	}
	var selectedImage *image
	for i := range images {
		if images[i].ID == c.config.ImageID && images[i].Region == c.config.Region {
			selectedImage = &images[i]
			break
		}
	}
	if selectedImage == nil || !strings.EqualFold(selectedImage.Status, "active") {
		return Plan{}, fmt.Errorf("selected image is not active in the region")
	}
	if !strings.Contains(strings.ToLower(selectedImage.Name), "ubuntu") || !strings.Contains(selectedImage.Name, "24.04") {
		return Plan{}, fmt.Errorf("selected image must be Ubuntu 24.04")
	}
	if selectedImage.User != "" && selectedImage.User != c.config.SSHUser {
		return Plan{}, fmt.Errorf("OVH_SSH_USER does not match the image login user")
	}
	var flavors []flavor
	if err := c.call(ctx, "GET", base+"/flavor"+query, nil, &flavors, true); err != nil {
		return Plan{}, fmt.Errorf("check flavors: %w", err)
	}
	var selectedFlavor *flavor
	for i := range flavors {
		if flavors[i].ID == c.config.FlavorID && flavors[i].Region == c.config.Region {
			selectedFlavor = &flavors[i]
			break
		}
	}
	if selectedFlavor == nil || !selectedFlavor.Available || selectedFlavor.Quota < 1 {
		return Plan{}, fmt.Errorf("selected flavor is unavailable or has no quota in the region")
	}
	// OVH reports flavor RAM in GiB and the OpenStack image minimum in MiB.
	minRAMGiB := selectedImage.MinRam / 1024
	if selectedImage.MinRam%1024 != 0 {
		minRAMGiB++
	}
	if selectedFlavor.Disk < selectedImage.MinDisk || selectedFlavor.Ram < minRAMGiB || (selectedImage.FlavorType != "" && selectedImage.FlavorType != selectedFlavor.Type) {
		return Plan{}, fmt.Errorf("selected image and flavor are incompatible")
	}
	var keys []sshKey
	if err := c.call(ctx, "GET", base+"/sshkey"+query, nil, &keys, true); err != nil {
		return Plan{}, fmt.Errorf("check SSH keys: %w", err)
	}
	var selectedKey *sshKey
	for i := range keys {
		if keys[i].ID == c.config.SSHKeyID && contains(keys[i].Regions, c.config.Region) {
			selectedKey = &keys[i]
			break
		}
	}
	if selectedKey == nil {
		return Plan{}, fmt.Errorf("selected SSH key is unavailable in the region")
	}
	return Plan{ProjectID: c.config.ProjectID, Region: c.config.Region, ImageID: c.config.ImageID, ImageName: selectedImage.Name, FlavorID: c.config.FlavorID, FlavorName: selectedFlavor.Name, SSHKeyID: c.config.SSHKeyID, SSHKeyName: selectedKey.Name, ServerName: c.config.ServerName, SSHUser: c.config.SSHUser, Ref: c.config.Ref}, nil
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func (c *Client) create(ctx context.Context) (instance, error) {
	payload := struct {
		Name           string `json:"name"`
		Region         string `json:"region"`
		ImageID        string `json:"imageId"`
		FlavorID       string `json:"flavorId"`
		SSHKeyID       string `json:"sshKeyId"`
		MonthlyBilling bool   `json:"monthlyBilling"`
	}{c.config.ServerName, c.config.Region, c.config.ImageID, c.config.FlavorID, c.config.SSHKeyID, false}
	var result instance
	err := c.call(ctx, "POST", c.projectPath()+"/instance", payload, &result, true)
	if err != nil {
		return result, err
	}
	if !identifier.MatchString(result.ID) {
		return result, fmt.Errorf("creation response did not contain a valid server ID")
	}
	return result, nil
}

func (c *Client) server(ctx context.Context, id string) (instance, error) {
	var result instance
	err := c.call(ctx, "GET", c.projectPath()+"/instance/"+url.PathEscape(id), nil, &result, true)
	return result, err
}
