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
	"sort"
	"strconv"
	"strings"
	"time"
)

// HTTPClient permits an in-memory fake; production uses an HTTP client that
// refuses redirects so authentication headers cannot be forwarded elsewhere.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Choice is a non-secret option presented when OVH has more than one valid
// candidate. IDs are stable API identifiers; Name and Detail are display-only.
type Choice struct {
	ID, Name, Detail string
}

// ChooseFunc is called only for genuine ambiguity. Returning an ID not present
// in choices is rejected rather than silently selecting a different resource.
type ChooseFunc func(context.Context, string, []Choice) (string, error)

type Client struct {
	config      Config
	http        HTTPClient
	clockOffset time.Duration
	timeSynced  bool
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
	if c.timeSynced {
		return nil
	}
	var serverTime int64
	before := time.Now()
	if err := c.call(ctx, http.MethodGet, "/auth/time", nil, &serverTime, false); err != nil {
		return fmt.Errorf("get OVH server time: %w", err)
	}
	if serverTime <= 0 {
		return fmt.Errorf("invalid OVH server time")
	}
	c.clockOffset = time.Unix(serverTime, 0).Sub(before.Add(time.Since(before) / 2))
	c.timeSynced = true
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

type flavorCapability struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type flavor struct {
	ID, Region, Name, Type  string
	Available               bool
	Disk, Ram, Vcpus, Quota int
	Capabilities            []flavorCapability `json:"capabilities"`
	Flex                    bool               `json:"flex"`
	IsFlex                  bool               `json:"isFlex"`
	Resizable               bool               `json:"resizable"`
}

type sshKey struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Fingerprint string   `json:"fingerPrint"`
	PublicKey   string   `json:"publicKey"`
	Regions     []string `json:"regions"`
}

type ipAddress struct {
	IP      string `json:"ip"`
	Type    string `json:"type"`
	Version int    `json:"version"`
}

type instance struct {
	ID          string      `json:"id"`
	Status      string      `json:"status"`
	IPAddresses []ipAddress `json:"ipAddresses"`
}

func (c *Client) projectPath() string { return c.projectPathFor(c.config.ProjectID) }

func (c *Client) projectPathFor(projectID string) string {
	return "/cloud/project/" + url.PathEscape(projectID)
}

// Preflight performs discovery and validation without creating a server. It
// automatically selects a unique project, region, image, and compatible
// flavor. Ambiguous candidates require a ChooseFunc when used by /ovh.
func (c *Client) Preflight(ctx context.Context) (Plan, error) {
	return c.PreflightWithSelector(ctx, nil)
}

func (c *Client) PreflightWithSelector(ctx context.Context, choose ChooseFunc) (Plan, error) {
	if err := c.syncTime(ctx); err != nil {
		return Plan{}, err
	}
	resolved := c.config

	projectID, err := c.resolveProject(ctx, resolved.ProjectID, choose)
	if err != nil {
		return Plan{}, err
	}
	base := c.projectPathFor(projectID)
	var project struct {
		ProjectID string `json:"project_id"`
		Status    string `json:"status"`
	}
	if err := c.call(ctx, http.MethodGet, base, nil, &project, true); err != nil {
		return Plan{}, fmt.Errorf("check project: %w", err)
	}
	if project.ProjectID != projectID || !strings.EqualFold(project.Status, "ok") {
		return Plan{}, fmt.Errorf("selected project is unavailable")
	}

	regionName, err := c.resolveRegion(ctx, base, resolved.Region, choose)
	if err != nil {
		return Plan{}, err
	}
	var region struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := c.call(ctx, http.MethodGet, base+"/region/"+url.PathEscape(regionName), nil, &region, true); err != nil {
		return Plan{}, fmt.Errorf("check region status: %w", err)
	}
	if region.Name != regionName || !strings.EqualFold(region.Status, "up") {
		return Plan{}, fmt.Errorf("selected region is unavailable")
	}

	query := "?region=" + url.QueryEscape(regionName)
	var images []image
	if err := c.call(ctx, http.MethodGet, base+"/image"+query, nil, &images, true); err != nil {
		return Plan{}, fmt.Errorf("check images: %w", err)
	}
	selectedImage, err := selectImage(images, regionName, resolved.ImageID, choose, ctx)
	if err != nil {
		return Plan{}, err
	}
	if resolved.userData == "" && resolved.SSHUser != "" && selectedImage.User != "" && selectedImage.User != resolved.SSHUser {
		return Plan{}, fmt.Errorf("OVH_SSH_USER does not match the image login user")
	}

	var flavors []flavor
	if err := c.call(ctx, http.MethodGet, base+"/flavor"+query, nil, &flavors, true); err != nil {
		return Plan{}, fmt.Errorf("check flavors: %w", err)
	}
	selectedFlavor, err := selectFlavor(flavors, regionName, *selectedImage, resolved.FlavorID, choose, ctx)
	if err != nil {
		return Plan{}, err
	}

	var keys []sshKey
	if err := c.call(ctx, http.MethodGet, base+"/sshkey"+query, nil, &keys, true); err != nil {
		return Plan{}, fmt.Errorf("check SSH keys: %w", err)
	}
	selectedKey, keyName, err := selectSSHKey(keys, regionName, resolved, choose, ctx)
	if err != nil {
		return Plan{}, err
	}
	if selectedKey != nil {
		resolved.SSHKeyID = selectedKey.ID
		keyName = selectedKey.Name
	} else {
		resolved.SSHKeyID = ""
	}

	if resolved.ServerName == "" {
		resolved.ServerName, err = NewServerName()
		if err != nil {
			return Plan{}, err
		}
	}
	if resolved.SSHUser == "" {
		resolved.SSHUser = DefaultSSHUser
	}
	if !sshUserPattern.MatchString(resolved.SSHUser) {
		return Plan{}, fmt.Errorf("SSH user has an invalid format")
	}
	if !commitID.MatchString(resolved.Ref) {
		return Plan{}, fmt.Errorf("GoAster commit is not a full lowercase 40-character commit ID")
	}
	if keyName == "" {
		keyName = defaultSSHKeyName
	}
	if !identifier.MatchString(keyName) {
		return Plan{}, fmt.Errorf("GoAster SSH key name is invalid")
	}
	resolved.ProjectID = projectID
	resolved.Region = regionName
	resolved.ImageID = selectedImage.ID
	resolved.FlavorID = selectedFlavor.ID
	resolved.sshKeyName = keyName
	c.config = resolved

	return Plan{
		ProjectID: projectID, Region: regionName,
		ImageID: selectedImage.ID, ImageName: selectedImage.Name,
		FlavorID: selectedFlavor.ID, FlavorName: selectedFlavor.Name,
		SSHKeyID: resolved.SSHKeyID, SSHKeyName: keyName,
		ServerName: resolved.ServerName, SSHUser: resolved.SSHUser, Ref: resolved.Ref,
	}, nil
}

func (c *Client) resolveProject(ctx context.Context, configured string, choose ChooseFunc) (string, error) {
	if configured != "" {
		return configured, nil
	}
	var projectIDs []string
	if err := c.call(ctx, http.MethodGet, "/cloud/project", nil, &projectIDs, true); err != nil {
		return "", fmt.Errorf("discover projects: %w", err)
	}
	sort.Strings(projectIDs)
	choices := make([]Choice, 0, len(projectIDs))
	seen := make(map[string]bool)
	for _, projectID := range projectIDs {
		if seen[projectID] || !identifier.MatchString(projectID) {
			continue
		}
		seen[projectID] = true
		var project struct {
			ProjectID string `json:"project_id"`
			Status    string `json:"status"`
		}
		if err := c.call(ctx, http.MethodGet, c.projectPathFor(projectID), nil, &project, true); err != nil {
			return "", fmt.Errorf("check discovered project: %w", err)
		}
		if project.ProjectID == projectID && strings.EqualFold(project.Status, "ok") {
			choices = append(choices, Choice{ID: projectID, Name: projectID})
		}
	}
	selected, err := chooseChoice(ctx, "Public Cloud projects", choices, choose)
	if err != nil {
		return "", err
	}
	return selected.ID, nil
}

func (c *Client) resolveRegion(ctx context.Context, base, configured string, choose ChooseFunc) (string, error) {
	var regionNames []string
	if err := c.call(ctx, http.MethodGet, base+"/region", nil, &regionNames, true); err != nil {
		return "", fmt.Errorf("discover regions: %w", err)
	}
	if configured != "" {
		if !contains(regionNames, configured) {
			return "", fmt.Errorf("selected region is not enabled for the project")
		}
		return configured, nil
	}
	sort.Strings(regionNames)
	choices := make([]Choice, 0, len(regionNames))
	seen := make(map[string]bool)
	for _, name := range regionNames {
		if seen[name] {
			continue
		}
		seen[name] = true
		var region struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		}
		if err := c.call(ctx, http.MethodGet, base+"/region/"+url.PathEscape(name), nil, &region, true); err != nil {
			return "", fmt.Errorf("check discovered region: %w", err)
		}
		if region.Name == name && strings.EqualFold(region.Status, "up") {
			choices = append(choices, Choice{ID: name, Name: name})
		}
	}
	selected, err := chooseChoice(ctx, "available regions", choices, choose)
	if err != nil {
		return "", err
	}
	return selected.ID, nil
}

func selectImage(images []image, region, configured string, choose ChooseFunc, ctx context.Context) (*image, error) {
	if configured != "" {
		for i := range images {
			if images[i].ID == configured && images[i].Region == region {
				if err := validateImage(images[i]); err != nil {
					return nil, err
				}
				return &images[i], nil
			}
		}
		return nil, fmt.Errorf("selected image is not active in the region")
	}
	candidates := make([]image, 0)
	for _, candidate := range images {
		if candidate.Region == region && isUbuntu2404(candidate) {
			candidates = append(candidates, candidate)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Name != candidates[j].Name {
			return candidates[i].Name < candidates[j].Name
		}
		return candidates[i].ID < candidates[j].ID
	})
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no active Ubuntu 24.04 image is available in the region")
	}
	selected, err := chooseChoice(ctx, "Ubuntu 24.04 images", imageChoices(candidates), choose)
	if err != nil {
		return nil, err
	}
	for i := range candidates {
		if candidates[i].ID == selected.ID {
			return &candidates[i], nil
		}
	}
	return nil, fmt.Errorf("selected image is unavailable")
}

func validateImage(candidate image) error {
	if !isUbuntu2404(candidate) {
		return fmt.Errorf("selected image must be an active Ubuntu 24.04 image")
	}
	return nil
}

func isUbuntu2404(candidate image) bool {
	name := strings.ToLower(candidate.Name)
	return strings.EqualFold(candidate.Status, "active") && strings.Contains(name, "ubuntu") && strings.Contains(name, "24.04")
}

func selectFlavor(flavors []flavor, region string, selectedImage image, configured string, choose ChooseFunc, ctx context.Context) (*flavor, error) {
	minRAMGiB := selectedImage.MinRam / 1024
	if selectedImage.MinRam%1024 != 0 {
		minRAMGiB++
	}
	if configured != "" {
		for i := range flavors {
			if flavors[i].ID == configured && flavors[i].Region == region {
				if !flavorCompatible(flavors[i], selectedImage, minRAMGiB) {
					return nil, fmt.Errorf("selected flavor is unavailable or incompatible with the image")
				}
				return &flavors[i], nil
			}
		}
		return nil, fmt.Errorf("selected flavor is unavailable or has no quota in the region")
	}
	candidates := make([]flavor, 0)
	for _, candidate := range flavors {
		if candidate.Region == region && flavorCompatible(candidate, selectedImage, minRAMGiB) {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no available OVH flavor is compatible with Ubuntu 24.04 and the project quota")
	}
	flexCandidates := make([]flavor, 0, len(candidates))
	for _, candidate := range candidates {
		if flavorIsFlex(candidate) {
			flexCandidates = append(flexCandidates, candidate)
		}
	}
	if len(flexCandidates) > 0 {
		candidates = flexCandidates
	}
	sort.Slice(candidates, func(i, j int) bool { return flavorLess(candidates[i], candidates[j]) })
	best := candidates[0]
	tied := []flavor{best}
	for _, candidate := range candidates[1:] {
		if sameFlavorSize(best, candidate) {
			tied = append(tied, candidate)
		} else {
			break
		}
	}
	selected, err := chooseChoice(ctx, "smallest compatible OVH flavors", flavorChoices(tied), choose)
	if err != nil {
		return nil, err
	}
	for i := range candidates {
		if candidates[i].ID == selected.ID {
			return &candidates[i], nil
		}
	}
	return nil, fmt.Errorf("selected flavor is unavailable")
}

func flavorCompatible(candidate flavor, selectedImage image, minRAMGiB int) bool {
	if !candidate.Available || candidate.Quota < 1 || candidate.Disk < selectedImage.MinDisk || candidate.Ram < minRAMGiB {
		return false
	}
	return selectedImage.FlavorType == "" || strings.EqualFold(selectedImage.FlavorType, candidate.Type)
}

func flavorIsFlex(candidate flavor) bool {
	if candidate.Flex || candidate.IsFlex || candidate.Resizable || strings.EqualFold(candidate.Type, "flex") || strings.EqualFold(candidate.Type, "flexible") {
		return true
	}
	for _, capability := range candidate.Capabilities {
		name := strings.ToLower(strings.TrimSpace(capability.Name))
		if capability.Enabled && (name == "flex" || name == "resize" || name == "resizable") {
			return true
		}
	}
	// OVH's documented catalog names use an explicit -flex suffix. It is safe
	// to honor that marker, while avoiding guesses based on flavor family.
	return strings.HasSuffix(strings.ToLower(candidate.Name), "-flex")
}

func flavorLess(left, right flavor) bool {
	if left.Ram != right.Ram {
		return left.Ram < right.Ram
	}
	if left.Disk != right.Disk {
		return left.Disk < right.Disk
	}
	if left.Vcpus != right.Vcpus {
		return left.Vcpus < right.Vcpus
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	return left.ID < right.ID
}

func sameFlavorSize(left, right flavor) bool {
	return left.Ram == right.Ram && left.Disk == right.Disk && left.Vcpus == right.Vcpus
}

func selectSSHKey(keys []sshKey, region string, config Config, choose ChooseFunc, ctx context.Context) (*sshKey, string, error) {
	if config.SSHKeyID != "" {
		for i := range keys {
			if keys[i].ID == config.SSHKeyID && keyAvailableInRegion(keys[i], region) {
				return &keys[i], keys[i].Name, nil
			}
		}
		return nil, "", fmt.Errorf("selected SSH key is unavailable in the region")
	}
	if config.publicKey == "" {
		return nil, "", fmt.Errorf("local GoAster public SSH key is not configured")
	}
	matches := make([]sshKey, 0)
	for _, key := range keys {
		if keyAvailableInRegion(key, region) && sshKeyMatches(key, config.publicKey) {
			matches = append(matches, key)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	if len(matches) == 0 {
		name := config.sshKeyName
		if name == "" {
			name = defaultSSHKeyName
		}
		return nil, name, nil
	}
	selected, err := chooseChoice(ctx, "matching GoAster SSH keys", sshKeyChoices(matches), choose)
	if err != nil {
		return nil, "", err
	}
	for i := range matches {
		if matches[i].ID == selected.ID {
			return &matches[i], matches[i].Name, nil
		}
	}
	return nil, "", fmt.Errorf("selected SSH key is unavailable")
}

func keyAvailableInRegion(key sshKey, region string) bool {
	return contains(key.Regions, region)
}

func sshKeyMatches(key sshKey, publicKey string) bool {
	want := publicKeyFingerprint(publicKey)
	if want == "" {
		return false
	}
	if key.PublicKey != "" {
		if got, err := normalizePublicKey(key.PublicKey); err == nil && publicKeyFingerprint(got) == want {
			return true
		}
	}
	fingerprint := strings.TrimSpace(key.Fingerprint)
	return strings.EqualFold(fingerprint, want) || strings.EqualFold("SHA256:"+fingerprint, want) || strings.EqualFold(fingerprint, publicKeyMD5Fingerprint(publicKey))
}

func chooseChoice(ctx context.Context, kind string, choices []Choice, choose ChooseFunc) (Choice, error) {
	if len(choices) == 0 {
		return Choice{}, fmt.Errorf("no usable %s are available", kind)
	}
	if len(choices) == 1 {
		return choices[0], nil
	}
	if choose == nil {
		return Choice{}, fmt.Errorf("ambiguous %s; choose one", kind)
	}
	ids := make(map[string]bool, len(choices))
	for _, choice := range choices {
		ids[choice.ID] = true
	}
	selected, err := choose(ctx, kind, choices)
	if err != nil {
		return Choice{}, fmt.Errorf("choose %s: %w", kind, err)
	}
	if !ids[selected] {
		return Choice{}, fmt.Errorf("choose %s: invalid choice", kind)
	}
	for _, choice := range choices {
		if choice.ID == selected {
			return choice, nil
		}
	}
	return Choice{}, fmt.Errorf("choose %s: invalid choice", kind)
}

func imageChoices(images []image) []Choice {
	choices := make([]Choice, 0, len(images))
	for _, image := range images {
		choices = append(choices, Choice{ID: image.ID, Name: image.Name})
	}
	return choices
}

func flavorChoices(flavors []flavor) []Choice {
	choices := make([]Choice, 0, len(flavors))
	for _, flavor := range flavors {
		flex := ""
		if flavorIsFlex(flavor) {
			flex = ", resizable Flex"
		}
		choices = append(choices, Choice{ID: flavor.ID, Name: flavor.Name, Detail: fmt.Sprintf("%d GiB RAM, %d GB disk, %d vCPU%s", flavor.Ram, flavor.Disk, flavor.Vcpus, flex)})
	}
	return choices
}

func sshKeyChoices(keys []sshKey) []Choice {
	choices := make([]Choice, 0, len(keys))
	for _, key := range keys {
		choices = append(choices, Choice{ID: key.ID, Name: key.Name})
	}
	return choices
}

// EnsureSSHKey registers the local public key only after the user has
// approved the billable server plan. Existing matching keys are reused.
func (c *Client) EnsureSSHKey(ctx context.Context, plan Plan) (Plan, error) {
	if plan.SSHKeyID != "" {
		return plan, nil
	}
	if c.config.publicKey == "" {
		return plan, fmt.Errorf("local GoAster public SSH key is not configured")
	}
	if err := c.syncTime(ctx); err != nil {
		return plan, err
	}
	name := plan.SSHKeyName
	if name == "" {
		name = defaultSSHKeyName
	}
	payload := struct {
		Name      string `json:"name"`
		PublicKey string `json:"publicKey"`
	}{Name: name, PublicKey: c.config.publicKey}
	var key sshKey
	if err := c.call(ctx, http.MethodPost, c.projectPath()+"/sshkey", payload, &key, true); err != nil {
		return plan, fmt.Errorf("upload GoAster SSH public key: %w", err)
	}
	if !identifier.MatchString(key.ID) {
		return plan, fmt.Errorf("upload response did not contain a valid SSH key ID")
	}
	if key.Name == "" {
		key.Name = name
	}
	plan.SSHKeyID = key.ID
	plan.SSHKeyName = key.Name
	c.config.SSHKeyID = key.ID
	c.config.sshKeyName = key.Name
	return plan, nil
}

func (c *Client) create(ctx context.Context) (instance, error) {
	payload := struct {
		Name           string `json:"name"`
		Region         string `json:"region"`
		ImageID        string `json:"imageId"`
		FlavorID       string `json:"flavorId"`
		SSHKeyID       string `json:"sshKeyId"`
		MonthlyBilling bool   `json:"monthlyBilling"`
		UserData       string `json:"userData,omitempty"`
	}{c.config.ServerName, c.config.Region, c.config.ImageID, c.config.FlavorID, c.config.SSHKeyID, false, c.config.userData}
	var result instance
	err := c.call(ctx, http.MethodPost, c.projectPath()+"/instance", payload, &result, true)
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
	err := c.call(ctx, http.MethodGet, c.projectPath()+"/instance/"+url.PathEscape(id), nil, &result, true)
	return result, err
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
