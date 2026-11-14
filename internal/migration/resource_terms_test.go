package migration

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testResourceTerms() ResourceTerms {
	return ResourceTerms{
		Version:      ResourceTermsVersion,
		ServerType:   ServerTypeTerms{ID: 22, Name: "cx22"},
		Image:        ImageTerms{ID: 123, Name: "ubuntu-24.04"},
		Architecture: "x86",
		Networking: NetworkingTerms{
			NetworkIDs:    []int64{42, 7},
			EnableIPv4:    true,
			EnableIPv6:    false,
			PrimaryIPType: "ipv4",
		},
		LocationPolicy: LocationPolicy{Mode: LocationPolicyAllowlist, Allowlist: []string{"nbg1", "fsn1"}},
		Operation:      OperationIdentity{ID: "migration-operation-01", Kind: ResourceOperationKindBillableCreate},
		BillableCreate: BillableCreateAcknowledgement{Acknowledged: true, Uncapped: true, CreateCount: 1},
	}
}

func TestResourceTermsBindingNormalizesSetLikeTerms(t *testing.T) {
	terms := testResourceTerms()
	first, err := terms.Binding()
	if err != nil {
		t.Fatal(err)
	}

	terms.Networking.NetworkIDs = []int64{7, 42}
	terms.LocationPolicy.Allowlist = []string{"fsn1", "nbg1"}
	second, err := terms.Binding()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("set order changed binding: first=%+v second=%+v", first, second)
	}

	canonical, err := terms.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	canonicalText := string(canonical)
	if !strings.Contains(canonicalText, `"network_ids":[7,42]`) || !strings.Contains(canonicalText, `"allowlist":["fsn1","nbg1"]`) {
		t.Fatalf("canonical terms were not normalized: %s", canonicalText)
	}
	if strings.Contains(canonicalText, "gross") || strings.Contains(canonicalText, "currency") || strings.Contains(canonicalText, "price") {
		t.Fatalf("price terms entered the non-price representation: %s", canonicalText)
	}

	anyLocation := testResourceTerms()
	anyLocation.LocationPolicy = LocationPolicy{Mode: LocationPolicyAnyEligible, Allowlist: []string{}}
	if _, err := anyLocation.Binding(); err != nil {
		t.Fatalf("any-eligible location policy was rejected: %v", err)
	}
}

func TestResourceTermsBindingMatchesCanonicalVector(t *testing.T) {
	const wantCanonicalJSON = `{"version":"v1","server_type":{"id":22,"name":"cx22"},"image":{"id":123,"name":"ubuntu-24.04"},"architecture":"x86","networking":{"network_ids":[7,42],"enable_ipv4":true,"enable_ipv6":false,"primary_ip_type":"ipv4"},"location_policy":{"mode":"allowlist","allowlist":["fsn1","nbg1"]},"operation":{"id":"migration-operation-01","kind":"billable_server_create"},"billable_create":{"acknowledged":true,"uncapped":true,"create_count":1}}`
	const wantDigest = "fd44f14930edb3f8d9ec2ae27b402cad1dc2091db822010986d615506d84af13"

	terms := testResourceTerms()
	canonical, err := terms.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != wantCanonicalJSON {
		t.Fatalf("canonical JSON = %s, want %s", canonical, wantCanonicalJSON)
	}

	binding, err := terms.Binding()
	if err != nil {
		t.Fatal(err)
	}
	want := ResourceTermsBinding{
		TermsVersion: ResourceTermsVersion,
		Algorithm:    ResourceTermsBindingAlgorithm,
		Digest:       wantDigest,
	}
	if binding != want {
		t.Fatalf("binding = %+v, want %+v", binding, want)
	}
}

func TestResourceTermsBindingCoversEveryNonPriceDimension(t *testing.T) {
	base, err := testResourceTerms().Binding()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(ResourceTerms) ResourceTerms
	}{
		{name: "server type", change: func(terms ResourceTerms) ResourceTerms { terms.ServerType.ID++; return terms }},
		{name: "image", change: func(terms ResourceTerms) ResourceTerms { terms.Image.Name = "ubuntu-24.04-minimal"; return terms }},
		{name: "architecture", change: func(terms ResourceTerms) ResourceTerms { terms.Architecture = "arm"; return terms }},
		{name: "networking", change: func(terms ResourceTerms) ResourceTerms { terms.Networking.EnableIPv6 = true; return terms }},
		{name: "location policy", change: func(terms ResourceTerms) ResourceTerms {
			terms.LocationPolicy.Allowlist = []string{"fsn1"}
			return terms
		}},
		{name: "operation identity", change: func(terms ResourceTerms) ResourceTerms { terms.Operation.ID = "migration-operation-02"; return terms }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			changed, err := test.change(testResourceTerms()).Binding()
			if err != nil {
				t.Fatal(err)
			}
			if changed == base {
				t.Fatalf("changing %s did not change binding", test.name)
			}
		})
	}

	for _, change := range []func(ResourceTerms) ResourceTerms{
		func(terms ResourceTerms) ResourceTerms { terms.BillableCreate.Acknowledged = false; return terms },
		func(terms ResourceTerms) ResourceTerms { terms.BillableCreate.Uncapped = false; return terms },
		func(terms ResourceTerms) ResourceTerms { terms.BillableCreate.CreateCount = 2; return terms },
	} {
		if _, err := change(testResourceTerms()).Binding(); !errors.Is(err, ErrInvalidResourceTerms) {
			t.Fatalf("invalid billable-create acknowledgement error = %v", err)
		}
	}
}

func TestBindResourceTermsSnapshotsAllSlices(t *testing.T) {
	terms := testResourceTerms()
	bound, err := BindResourceTerms(terms)
	if err != nil {
		t.Fatal(err)
	}

	terms.Networking.NetworkIDs[0] = 999
	terms.LocationPolicy.Allowlist[0] = "hel1"

	if err := bound.Verify(); err != nil {
		t.Fatalf("post-bind source mutation changed the bound terms: %v", err)
	}
	if got, want := bound.Terms.Networking.NetworkIDs, []int64{42, 7}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("bound network IDs = %v, want %v", got, want)
	}
	if got, want := bound.Terms.LocationPolicy.Allowlist, []string{"nbg1", "fsn1"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("bound location allow-list = %v, want %v", got, want)
	}
}

func TestResourceTermsRequireVersionAndCompletePolicy(t *testing.T) {
	cases := []func(ResourceTerms) ResourceTerms{
		func(terms ResourceTerms) ResourceTerms { terms.Version = "v2"; return terms },
		func(terms ResourceTerms) ResourceTerms { terms.Operation.ID = ""; return terms },
		func(terms ResourceTerms) ResourceTerms { terms.Operation.Kind = "availability"; return terms },
		func(terms ResourceTerms) ResourceTerms {
			terms.LocationPolicy = LocationPolicy{Mode: LocationPolicyAnyEligible, Allowlist: []string{"fsn1"}}
			return terms
		},
		func(terms ResourceTerms) ResourceTerms {
			terms.LocationPolicy = LocationPolicy{Mode: LocationPolicyAllowlist}
			return terms
		},
		func(terms ResourceTerms) ResourceTerms { terms.Networking.NetworkIDs = []int64{7, 7}; return terms },
	}
	for index, change := range cases {
		if _, err := change(testResourceTerms()).Binding(); !errors.Is(err, ErrInvalidResourceTerms) {
			t.Fatalf("case %d validation error = %v", index, err)
		}
	}
}

func TestBoundResourceTermsVerifiesExactTerms(t *testing.T) {
	bound, err := BindResourceTerms(testResourceTerms())
	if err != nil {
		t.Fatal(err)
	}
	if err := bound.Verify(); err != nil {
		t.Fatal(err)
	}

	changed := bound.Terms
	changed.Operation.ID = "migration-operation-02"
	if err := VerifyResourceTermsBinding(changed, bound.Binding); !errors.Is(err, ErrResourceTermsBindingMismatch) {
		t.Fatalf("changed operation was accepted: %v", err)
	}

	encoded, err := json.Marshal(bound)
	if err != nil {
		t.Fatal(err)
	}
	encodedText := string(encoded)
	for _, field := range []string{"version", "server_type", "image", "architecture", "networking", "location_policy", "operation", "billable_create", "binding", "digest"} {
		if !strings.Contains(encodedText, `"`+field+`"`) {
			t.Fatalf("bound terms omitted %q: %s", field, encodedText)
		}
	}
	if strings.Contains(encodedText, "gross") || strings.Contains(encodedText, "currency") {
		t.Fatalf("bound terms contain price fields: %s", encodedText)
	}
}
