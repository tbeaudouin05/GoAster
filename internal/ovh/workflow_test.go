package ovh

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const testRef = "0123456789abcdef0123456789abcdef01234567"

func testValues() map[string]string {
	return map[string]string{
		"OVH_ENDPOINT": "ovh-eu", "OVH_APPLICATION_KEY": "fake-app-key", "OVH_APPLICATION_SECRET": "fake-app-secret", "OVH_CONSUMER_KEY": "fake-consumer-secret",
		"OVH_PROJECT_ID": "project-1", "OVH_REGION": "GRA11", "OVH_IMAGE_ID": "image-1", "OVH_FLAVOR_ID": "flavor-1", "OVH_SSH_KEY_ID": "key-1",
		"OVH_SERVER_NAME": "agent-1", "OVH_SSH_USER": "ubuntu", "OVH_SSH_PRIVATE_KEY": "/local/fake-key", "OVH_GOASTER_REF": testRef,
	}
}

type fakeHTTP struct {
	t         *testing.T
	confirmed *bool
	requests  []string
	fail      string
	responses map[string]string
	polls     int
}

func (f *fakeHTTP) Do(r *http.Request) (*http.Response, error) {
	f.t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	key := r.Method + " " + r.URL.Path
	f.requests = append(f.requests, key)
	if r.URL.Host != "eu.api.ovh.com" || r.Header.Get("X-Ovh-Application") != "fake-app-key" {
		f.t.Error("unexpected host or application header")
	}
	if r.URL.Path != "/1.0/auth/time" {
		timestamp := r.Header.Get("X-Ovh-Timestamp")
		expected := sha1.Sum([]byte(strings.Join([]string{"fake-app-secret", "fake-consumer-secret", r.Method, r.URL.String(), string(body), timestamp}, "+")))
		if timestamp == "" || r.Header.Get("X-Ovh-Consumer") != "fake-consumer-secret" || r.Header.Get("X-Ovh-Signature") != fmt.Sprintf("$1$%x", expected) {
			f.t.Error("incorrect OVH authentication headers")
		}
	}
	if strings.Contains(key, "/image") || strings.Contains(key, "/flavor") || strings.Contains(key, "/sshkey") {
		if r.URL.Query().Get("region") != "GRA11" {
			f.t.Error("missing regional query")
		}
	}
	if r.Method == "DELETE" {
		f.t.Error("workflow must never delete a server")
	}
	if r.Method == "POST" {
		if f.confirmed == nil || !*f.confirmed {
			f.t.Error("created without explicit confirmation")
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			f.t.Fatal("invalid creation JSON")
		}
		for k, want := range map[string]any{"name": "agent-1", "region": "GRA11", "imageId": "image-1", "flavorId": "flavor-1", "sshKeyId": "key-1", "monthlyBilling": false} {
			if payload[k] != want {
				f.t.Errorf("unexpected creation parameter %s", k)
			}
		}
		if len(payload) != 6 {
			f.t.Error("unexpected creation parameters")
		}
	}
	if key == f.fail {
		return nil, errors.New("fake-app-secret fake-consumer-secret")
	}
	var value string
	if override, ok := f.responses[key]; ok {
		value = override
	} else {
		switch key {
		case "GET /1.0/auth/time":
			value = fmt.Sprintf("%d", time.Now().Unix())
		case "GET /1.0/cloud/project/project-1":
			value = `{"project_id":"project-1","status":"ok"}`
		case "GET /1.0/cloud/project/project-1/region":
			value = `["GRA11"]`
		case "GET /1.0/cloud/project/project-1/region/GRA11":
			value = `{"name":"GRA11","status":"UP"}`
		case "GET /1.0/cloud/project/project-1/image":
			value = `[{"id":"image-1","region":"GRA11","name":"Ubuntu 24.04","status":"active","user":"ubuntu","minDisk":10,"minRam":2048}]`
		case "GET /1.0/cloud/project/project-1/flavor":
			value = `[{"id":"flavor-1","region":"GRA11","name":"b2-7","available":true,"quota":1,"disk":20,"ram":8}]`
		case "GET /1.0/cloud/project/project-1/sshkey":
			value = `[{"id":"key-1","name":"my-key","regions":["GRA11"]}]`
		case "POST /1.0/cloud/project/project-1/instance":
			value = `{"id":"server-1","status":"BUILD"}`
		case "GET /1.0/cloud/project/project-1/instance/server-1":
			f.polls++
			switch f.polls {
			case 1:
				value = `{"id":"server-1","status":"BUILD"}`
			case 2:
				value = `{"id":"server-1","status":"ACTIVE","ipAddresses":[{"ip":"10.0.0.2","type":"private"}]}`
			default:
				value = `{"id":"server-1","status":"ACTIVE","ipAddresses":[{"ip":"203.0.113.7","type":"public"}]}`
			}
		default:
			f.t.Errorf("unexpected fake HTTP request %s", key)
			value = `{}`
		}
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(value)), Header: make(http.Header)}, nil
}

type fakeSSH struct {
	checks int
	fail   bool
}

func (s *fakeSSH) Check(_ context.Context, target Target) error {
	s.checks++
	if target.IP != "203.0.113.7" || target.User != "ubuntu" || target.PrivateKey != "/local/fake-key" {
		return errors.New("unexpected target")
	}
	if s.fail || s.checks == 1 {
		return errors.New("fake-consumer-secret")
	}
	return nil
}

type fakeRemote struct {
	calls int
	fail  bool
}

func (r *fakeRemote) Install(_ context.Context, target Target, ref string) (string, string, error) {
	r.calls++
	if target.IP != "203.0.113.7" || ref != testRef {
		return "", "", errors.New("invalid install inputs")
	}
	if r.fail {
		return "", "", errors.New("fake-app-secret")
	}
	return ref, "active", nil
}

func TestWorkflowSuccessAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name, failHTTP, wantError     string
		confirm, failSSH, failInstall bool
		wantID, wantIP                string
		wantPost, wantInstall         bool
	}{
		{name: "success", confirm: true, wantID: "server-1", wantIP: "203.0.113.7", wantPost: true, wantInstall: true},
		{name: "refusal", wantError: "not confirmed"},
		{name: "preflight API failure", confirm: true, failHTTP: "GET /1.0/cloud/project/project-1/flavor", wantError: "preflight"},
		{name: "ambiguous creation failure", confirm: true, failHTTP: "POST /1.0/cloud/project/project-1/instance", wantError: "outcome may be unknown", wantPost: true},
		{name: "status API failure", confirm: true, failHTTP: "GET /1.0/cloud/project/project-1/instance/server-1", wantError: "server status", wantID: "server-1", wantPost: true},
		{name: "SSH failure", confirm: true, failSSH: true, wantError: "SSH readiness", wantID: "server-1", wantIP: "203.0.113.7", wantPost: true},
		{name: "install failure", confirm: true, failInstall: true, wantError: "install or verify", wantID: "server-1", wantIP: "203.0.113.7", wantPost: true, wantInstall: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, err := ConfigFromValues(testValues())
			if err != nil {
				t.Fatal(err)
			}
			confirmed := false
			fake := &fakeHTTP{t: t, confirmed: &confirmed, fail: tc.failHTTP}
			client, err := NewClient(config, fake)
			if err != nil {
				t.Fatal(err)
			}
			ssh := &fakeSSH{fail: tc.failSSH}
			remote := &fakeRemote{fail: tc.failInstall}
			var created Result
			w := Workflow{Client: client, SSH: ssh, Remote: remote, Timeout: 30 * time.Millisecond, PollInterval: time.Millisecond,
				Confirm: func(_ context.Context, plan Plan) (bool, error) {
					if plan.ProjectID != "project-1" || plan.Region != "GRA11" || plan.Ref != testRef || plan.ImageID != "image-1" || plan.FlavorID != "flavor-1" || plan.SSHKeyID != "key-1" {
						t.Error("incorrect confirmation plan")
					}
					confirmed = tc.confirm
					return tc.confirm, nil
				}, Created: func(result Result) error { created = result; return nil },
			}
			result, err := w.Run(context.Background())
			if tc.wantError == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("error = %v, want %s", err, tc.wantError)
			}
			if err != nil && (strings.Contains(err.Error(), "fake-app-secret") || strings.Contains(err.Error(), "fake-consumer-secret")) {
				t.Fatal("error exposed fake credentials")
			}
			if result.ServerID != tc.wantID || result.IP != tc.wantIP || result.CreationAttempted != tc.wantPost {
				t.Errorf("unexpected partial result: %+v", result)
			}
			if tc.wantID != "" && created.ServerID != tc.wantID {
				t.Error("server ID was not reported immediately")
			}
			if tc.wantError == "" && (result.Commit != testRef || result.ServiceStatus != "active" || ssh.checks != 2 || fake.polls != 3) {
				t.Errorf("unexpected successful result or polling: %+v", result)
			}
			if (remote.calls > 0) != tc.wantInstall {
				t.Errorf("remote installation calls = %d", remote.calls)
			}
			posted := false
			for _, request := range fake.requests {
				if strings.HasPrefix(request, "POST ") {
					posted = true
				}
			}
			if posted != tc.wantPost {
				t.Errorf("server creation request = %v", posted)
			}
		})
	}
}

func TestPreflightRejectsInvalidSelectionsBeforeConfirmation(t *testing.T) {
	for _, tc := range []struct{ name, path, response string }{
		{"project", "", `{"project_id":"project-1","status":"suspended"}`},
		{"region", "/region", `["OTHER"]`},
		{"region down", "/region/GRA11", `{"name":"GRA11","status":"DOWN"}`},
		{"image", "/image", `[{"id":"image-1","region":"OTHER","name":"Ubuntu 24.04","status":"active"}]`},
		{"flavor", "/flavor", `[{"id":"flavor-1","region":"GRA11","available":false,"quota":1}]`},
		{"key", "/sshkey", `[{"id":"key-1","regions":["OTHER"]}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, _ := ConfigFromValues(testValues())
			f := &fakeHTTP{t: t, responses: map[string]string{"GET /1.0/cloud/project/project-1" + tc.path: tc.response}}
			client, _ := NewClient(config, f)
			called := false
			w := Workflow{Client: client, SSH: &fakeSSH{}, Remote: &fakeRemote{}, Confirm: func(context.Context, Plan) (bool, error) { called = true; return true, nil }}
			if _, err := w.Run(context.Background()); err == nil {
				t.Fatal("preflight accepted invalid selection")
			}
			if called {
				t.Fatal("confirmation was called for an invalid plan")
			}
		})
	}
}

func TestConfigValidationAndRedaction(t *testing.T) {
	for _, key := range []string{"OVH_ENDPOINT", "OVH_APPLICATION_KEY", "OVH_APPLICATION_SECRET", "OVH_CONSUMER_KEY", "OVH_PROJECT_ID", "OVH_REGION", "OVH_IMAGE_ID", "OVH_FLAVOR_ID", "OVH_SSH_KEY_ID", "OVH_SERVER_NAME", "OVH_SSH_USER", "OVH_SSH_PRIVATE_KEY", "OVH_GOASTER_REF"} {
		values := testValues()
		delete(values, key)
		if _, err := ConfigFromValues(values); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("missing %s accepted", key)
		}
	}
	for key, value := range map[string]string{"OVH_ENDPOINT": "https://example.invalid", "OVH_PROJECT_ID": "../other", "OVH_SSH_USER": "-o", "OVH_SSH_PRIVATE_KEY": "relative/path", "OVH_GOASTER_REF": "main", "OVH_CONSUMER_KEY": "replace-with-secret"} {
		values := testValues()
		values[key] = value
		if _, err := ConfigFromValues(values); err == nil {
			t.Errorf("invalid %s accepted", key)
		}
	}
	config, err := ConfigFromValues(testValues())
	if err != nil {
		t.Fatal(err)
	}
	for _, representation := range []string{fmt.Sprintf("%v", config), fmt.Sprintf("%+v", config), fmt.Sprintf("%#v", config)} {
		if strings.Contains(representation, "fake-app-secret") || strings.Contains(representation, "fake-consumer-secret") {
			t.Fatal("formatted configuration exposed fake credentials")
		}
	}
}

func TestRequiresConfirmationAndPreservesCreatedServerWhenReportingFails(t *testing.T) {
	config, _ := ConfigFromValues(testValues())
	confirmed := false
	f := &fakeHTTP{t: t, confirmed: &confirmed}
	client, _ := NewClient(config, f)
	w := Workflow{Client: client, SSH: &fakeSSH{}, Remote: &fakeRemote{}}
	if _, err := w.Run(context.Background()); err == nil || len(f.requests) != 0 {
		t.Fatal("missing callback should fail before API calls")
	}
	w.Confirm = func(context.Context, Plan) (bool, error) { confirmed = true; return true, nil }
	w.Created = func(Result) error { return errors.New("fake-app-secret") }
	result, err := w.Run(context.Background())
	if err == nil || result.ServerID != "server-1" || strings.Contains(err.Error(), "fake-app-secret") {
		t.Fatal("failed reporting did not preserve the ID safely")
	}
}

func TestPublicIPSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addresses []ipAddress
		want      string
	}{
		{"prefer ipv4", []ipAddress{{IP: "2001:db8::7/64", Type: "public"}, {IP: "203.0.113.7/32", Type: "public"}}, "203.0.113.7"},
		{"ipv6", []ipAddress{{IP: "2001:db8::7/64", Type: "public"}}, "2001:db8::7"},
		{"reject private", []ipAddress{{IP: "192.168.1.2", Type: "public"}, {IP: "203.0.113.7", Type: "private"}, {IP: "not-an-ip", Type: "public"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := publicIP(tc.addresses); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
