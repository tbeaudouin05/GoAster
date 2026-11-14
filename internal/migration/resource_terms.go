package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

const (
	// ResourceTermsVersion is the version of the local, non-price resource-term
	// representation. It is not a claim about a GoAsterWeb wire version.
	ResourceTermsVersion = "v1"

	// ResourceTermsBindingAlgorithm identifies the canonical local binding
	// encoding. The version is part of the algorithm name so a future encoding
	// change cannot silently reuse an old digest.
	ResourceTermsBindingAlgorithm = "sha256-canonical-json-v1"

	// ResourceOperationKindBillableCreate is the only operation kind accepted
	// by the local migration terms representation.
	ResourceOperationKindBillableCreate = "billable_server_create"

	// LocationPolicyAnyEligible and LocationPolicyAllowlist make the empty
	// location policy explicit instead of relying on nil-versus-empty JSON.
	LocationPolicyAnyEligible = "any_eligible"
	LocationPolicyAllowlist   = "allowlist"

	maxResourceNetworks = 64
)

var (
	// ErrInvalidResourceTerms means that the local terms are incomplete or
	// contain an unsupported value. It never includes term contents.
	ErrInvalidResourceTerms = errors.New("invalid migration resource terms")

	// ErrResourceTermsBindingMismatch means that a binding was not produced for
	// the exact validated local terms supplied for verification.
	ErrResourceTermsBindingMismatch = errors.New("migration resource terms binding mismatch")
)

// ServerTypeTerms identifies both the provider server-type ID and its name.
// Keeping both values prevents a local approval from silently changing when a
// provider-side name or ID is reused.
type ServerTypeTerms struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ImageTerms identifies both the provider image ID and its name.
type ImageTerms struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// NetworkingTerms contains the locally selected network attachment and public
// networking terms. PrimaryIPType is intentionally opaque here; its provider
// meaning must be established by a future explicit wire contract.
type NetworkingTerms struct {
	NetworkIDs    []int64 `json:"network_ids"`
	EnableIPv4    bool    `json:"enable_ipv4"`
	EnableIPv6    bool    `json:"enable_ipv6"`
	PrimaryIPType string  `json:"primary_ip_type"`
}

// LocationPolicy is explicit about whether an empty allow-list means any
// eligible location. Allowlist order is not semantically significant and is
// sorted before binding.
type LocationPolicy struct {
	Mode      string   `json:"mode"`
	Allowlist []string `json:"allowlist"`
}

// OperationIdentity identifies the one local operation to which the terms
// belong. The ID is local-only; it is not sent to GoAsterWeb by this increment.
type OperationIdentity struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// BillableCreateAcknowledgement records explicit acceptance of exactly one
// billable create without a price cap. It deliberately contains no price or
// currency field.
type BillableCreateAcknowledgement struct {
	Acknowledged bool `json:"acknowledged"`
	Uncapped     bool `json:"uncapped"`
	CreateCount  int  `json:"create_count"`
}

// ResourceTerms is the versioned local representation of all non-price
// resource terms that can currently be carried safely without asserting a Web
// contract. It is deliberately not used by the current v1 Web requests,
// scheduler message, model tool schema, or disabled runtime.
type ResourceTerms struct {
	Version        string                        `json:"version"`
	ServerType     ServerTypeTerms               `json:"server_type"`
	Image          ImageTerms                    `json:"image"`
	Architecture   string                        `json:"architecture"`
	Networking     NetworkingTerms               `json:"networking"`
	LocationPolicy LocationPolicy                `json:"location_policy"`
	Operation      OperationIdentity             `json:"operation"`
	BillableCreate BillableCreateAcknowledgement `json:"billable_create"`
}

// ResourceTermsBinding is the deterministic digest of one canonical
// ResourceTerms value. It contains no credentials, provider response, price,
// or other secret material.
type ResourceTermsBinding struct {
	TermsVersion string `json:"terms_version"`
	Algorithm    string `json:"algorithm"`
	Digest       string `json:"digest"`
}

// BoundResourceTerms is a local terms envelope for callers that need to carry
// the exact terms and their binding together. It has no Web serialization
// meaning until a separate compatible contract is defined.
type BoundResourceTerms struct {
	Terms   ResourceTerms        `json:"terms"`
	Binding ResourceTermsBinding `json:"binding"`
}

// Validate checks that every required local term is present and unambiguous.
// Validation is intentionally provider-agnostic where the current repository
// has no safe provider/Web contract, such as the image and primary-IP names.
func (terms ResourceTerms) Validate() error {
	if terms.Version != ResourceTermsVersion {
		return ErrInvalidResourceTerms
	}
	if terms.ServerType.ID <= 0 || !safeMachineName(terms.ServerType.Name, 128) {
		return ErrInvalidResourceTerms
	}
	if terms.Image.ID <= 0 || !safeMachineName(terms.Image.Name, 128) {
		return ErrInvalidResourceTerms
	}
	if !safeMachineName(terms.Architecture, 64) {
		return ErrInvalidResourceTerms
	}
	if err := validateNetworkingTerms(terms.Networking); err != nil {
		return err
	}
	if err := validateLocationPolicy(terms.LocationPolicy); err != nil {
		return err
	}
	if !safeMachineName(terms.Operation.ID, 128) || terms.Operation.Kind != ResourceOperationKindBillableCreate {
		return ErrInvalidResourceTerms
	}
	if !terms.BillableCreate.Acknowledged || !terms.BillableCreate.Uncapped || terms.BillableCreate.CreateCount != 1 {
		return ErrInvalidResourceTerms
	}
	return nil
}

func validateNetworkingTerms(networking NetworkingTerms) error {
	if len(networking.NetworkIDs) > maxResourceNetworks || (networking.PrimaryIPType != "" && !safeMachineName(networking.PrimaryIPType, 64)) {
		return ErrInvalidResourceTerms
	}
	seen := make(map[int64]struct{}, len(networking.NetworkIDs))
	for _, networkID := range networking.NetworkIDs {
		if networkID <= 0 {
			return ErrInvalidResourceTerms
		}
		if _, exists := seen[networkID]; exists {
			return ErrInvalidResourceTerms
		}
		seen[networkID] = struct{}{}
	}
	return nil
}

func validateLocationPolicy(policy LocationPolicy) error {
	if len(policy.Allowlist) > maxMigrationLocations {
		return ErrInvalidResourceTerms
	}
	seen := make(map[string]struct{}, len(policy.Allowlist))
	for _, location := range policy.Allowlist {
		if !safeMachineName(location, 128) {
			return ErrInvalidResourceTerms
		}
		if _, exists := seen[location]; exists {
			return ErrInvalidResourceTerms
		}
		seen[location] = struct{}{}
	}
	switch policy.Mode {
	case LocationPolicyAnyEligible:
		if len(policy.Allowlist) != 0 {
			return ErrInvalidResourceTerms
		}
	case LocationPolicyAllowlist:
		if len(policy.Allowlist) == 0 {
			return ErrInvalidResourceTerms
		}
	default:
		return ErrInvalidResourceTerms
	}
	return nil
}

// CanonicalJSON returns the stable, order-normalized JSON representation used
// for binding. Network IDs and location allow-list entries are set-like and
// are sorted; all other fields retain their explicit value.
func (terms ResourceTerms) CanonicalJSON() ([]byte, error) {
	if err := terms.Validate(); err != nil {
		return nil, err
	}
	canonical := terms
	canonical.Networking.NetworkIDs = append([]int64{}, terms.Networking.NetworkIDs...)
	sort.Slice(canonical.Networking.NetworkIDs, func(left, right int) bool {
		return canonical.Networking.NetworkIDs[left] < canonical.Networking.NetworkIDs[right]
	})
	canonical.LocationPolicy.Allowlist = append([]string{}, terms.LocationPolicy.Allowlist...)
	sort.Strings(canonical.LocationPolicy.Allowlist)
	return json.Marshal(canonical)
}

// Binding computes the deterministic, unkeyed SHA-256 binding for all
// validated local resource terms, including operation identity and the
// uncapped-create acknowledgement. It is not authentication or owner
// approval.
func (terms ResourceTerms) Binding() (ResourceTermsBinding, error) {
	canonical, err := terms.CanonicalJSON()
	if err != nil {
		return ResourceTermsBinding{}, err
	}
	digest := sha256.Sum256(canonical)
	return ResourceTermsBinding{
		TermsVersion: terms.Version,
		Algorithm:    ResourceTermsBindingAlgorithm,
		Digest:       hex.EncodeToString(digest[:]),
	}, nil
}

func snapshotResourceTerms(terms ResourceTerms) ResourceTerms {
	snapshot := terms
	if terms.Networking.NetworkIDs != nil {
		snapshot.Networking.NetworkIDs = append([]int64{}, terms.Networking.NetworkIDs...)
	}
	if terms.LocationPolicy.Allowlist != nil {
		snapshot.LocationPolicy.Allowlist = append([]string{}, terms.LocationPolicy.Allowlist...)
	}
	return snapshot
}

// BindResourceTerms builds a local envelope containing exact terms and their
// deterministic binding.
func BindResourceTerms(terms ResourceTerms) (BoundResourceTerms, error) {
	snapshot := snapshotResourceTerms(terms)
	binding, err := snapshot.Binding()
	if err != nil {
		return BoundResourceTerms{}, err
	}
	return BoundResourceTerms{Terms: snapshot, Binding: binding}, nil
}

// VerifyResourceTermsBinding verifies that binding matches the exact
// canonicalized terms. A malformed or incomplete terms value also fails.
func VerifyResourceTermsBinding(terms ResourceTerms, binding ResourceTermsBinding) error {
	expected, err := terms.Binding()
	if err != nil {
		return err
	}
	if expected != binding {
		return ErrResourceTermsBindingMismatch
	}
	return nil
}

// Verify checks both the terms and their attached binding.
func (bound BoundResourceTerms) Verify() error {
	return VerifyResourceTermsBinding(bound.Terms, bound.Binding)
}
