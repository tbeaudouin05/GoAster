package chat

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goaster/internal/localenv"
	"goaster/internal/ovh"
)

const fakeRef = "0123456789abcdef0123456789abcdef01234567"

type onboardingHTTP struct{ posts int }

func (f *onboardingHTTP) Do(r *http.Request) (*http.Response, error) {
	var body string
	switch r.Method + " " + r.URL.Path {
	case "GET /1.0/auth/time":
		body = fmt.Sprint(time.Now().Unix())
	case "GET /1.0/cloud/project/project":
		body = `{"project_id":"project","status":"ok"}`
	case "GET /1.0/cloud/project/project/region":
		body = `["GRA11"]`
	case "GET /1.0/cloud/project/project/region/GRA11":
		body = `{"name":"GRA11","status":"UP"}`
	case "GET /1.0/cloud/project/project/image":
		body = `[{"id":"image","region":"GRA11","name":"Ubuntu 24.04","status":"active","user":"ubuntu"}]`
	case "GET /1.0/cloud/project/project/flavor":
		body = `[{"id":"flavor","region":"GRA11","name":"test-flavor","available":true,"quota":1,"ram":2,"disk":20}]`
	case "GET /1.0/cloud/project/project/sshkey":
		body = `[{"id":"key","name":"test-key","regions":["GRA11"]}]`
	case "POST /1.0/cloud/project/project/instance":
		f.posts++
		body = `{"id":"server","status":"BUILD"}`
	case "GET /1.0/cloud/project/project/instance/server":
		body = `{"id":"server","status":"ACTIVE","ipAddresses":[{"ip":"203.0.113.7","type":"public"}]}`
	default:
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}

type onboardingRunners struct{ installs int }

func (*onboardingRunners) Check(context.Context, ovh.Target) error { return nil }
func (r *onboardingRunners) Install(_ context.Context, target ovh.Target, ref string) (string, string, error) {
	r.installs++
	if target.IP != "203.0.113.7" {
		return "", "", fmt.Errorf("unexpected fake IP")
	}
	return ref, "active", nil
}

func TestOVHLocalConfirmationAndReporting(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		creates      bool
	}{{"refuse", "no", false}, {"approve", "CREATE", true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			keyPath := filepath.Join(dir, "key")
			if err := os.WriteFile(keyPath, []byte("fake-key-content"), 0600); err != nil {
				t.Fatal(err)
			}
			store := localenv.NewStoreAt(filepath.Join(dir, "env"))
			values := map[string]string{"OVH_ENDPOINT": "ovh-eu", "OVH_APPLICATION_KEY": "fake-app-key", "OVH_APPLICATION_SECRET": "fake-app-secret", "OVH_CONSUMER_KEY": "fake-consumer-secret", "OVH_PROJECT_ID": "project", "OVH_REGION": "GRA11", "OVH_IMAGE_ID": "image", "OVH_FLAVOR_ID": "flavor", "OVH_SSH_KEY_ID": "key", "OVH_SERVER_NAME": "server-name", "OVH_SSH_USER": "ubuntu", "OVH_SSH_PRIVATE_KEY": keyPath, "OVH_GOASTER_REF": fakeRef}
			if err := store.Save(values); err != nil {
				t.Fatal(err)
			}
			fake := &onboardingHTTP{}
			runners := &onboardingRunners{}
			var output bytes.Buffer
			if err := runOVHSetupWithDependencies(context.Background(), bufio.NewScanner(strings.NewReader(tc.answer+"\n")), bufio.NewWriter(&output), store, fake, runners, runners); err != nil {
				t.Fatal(err)
			}
			if (fake.posts > 0) != tc.creates || (runners.installs > 0) != tc.creates {
				t.Fatal("confirmation did not gate creation and installation")
			}
			if !strings.Contains(output.String(), "Type CREATE") {
				t.Fatal("missing explicit confirmation prompt")
			}
			if tc.creates {
				for _, want := range []string{"server_id=server", "public_ip=203.0.113.7", "commit=" + fakeRef, "service=active"} {
					if !strings.Contains(output.String(), want) {
						t.Errorf("missing %s", want)
					}
				}
			} else if !strings.Contains(output.String(), "not confirmed") {
				t.Error("refusal not reported")
			}
			for _, secret := range []string{"fake-app-key", "fake-app-secret", "fake-consumer-secret", "fake-key-content", keyPath} {
				if strings.Contains(output.String(), secret) {
					t.Error("local output exposed a fake credential")
				}
			}
		})
	}
}
