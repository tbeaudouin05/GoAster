package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseCodexJSONFunctionCall(t *testing.T) {
	body := []byte(`{"output":[{"type":"function_call","call_id":"call-json","name":"web_search","arguments":"{\"query\":\"Go & Go\",\"count\":2,\"range\":\"w\"}"}]}`)
	parsed, err := parseCodexResponseResult(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v, want one call", parsed.ToolCalls)
	}
	call := parsed.ToolCalls[0]
	if call.CallID != "call-json" || call.Name != "web_search" || call.Arguments != `{"query":"Go & Go","count":2,"range":"w"}` {
		t.Fatalf("function call = %+v", call)
	}
}

func TestParseCodexSSEFunctionCall(t *testing.T) {
	body := strings.Join([]string{
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","item":{"id":"item-1","type":"function_call","call_id":"call-sse","name":"web_search","arguments":""}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","item_id":"item-1","delta":"{\"query\":\"golang"}`,
		"",
		"event: response.function_call_arguments.done",
		`data: {"type":"response.function_call_arguments.done","item_id":"item-1","arguments":"{\"query\":\"golang\",\"count\":2}"}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","item":{"id":"item-1","type":"function_call","call_id":"call-sse","name":"web_search","arguments":"{\"query\":\"golang\",\"count\":2}"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed"}`,
		"",
	}, "\n")

	parsed, err := parseCodexResponseResult([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v, want one deduplicated call", parsed.ToolCalls)
	}
	if parsed.ToolCalls[0].CallID != "call-sse" || parsed.ToolCalls[0].Name != "web_search" || parsed.ToolCalls[0].Arguments != `{"query":"golang","count":2}` {
		t.Fatalf("function call = %+v", parsed.ToolCalls[0])
	}
}

func TestParseDuckDuckGoResults(t *testing.T) {
	body := []byte(`
<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fone%3Fa%3D1%26b%3D2&amp;rut=one">First <b>Result</b></a>
<a class="result__snippet" href="//duckduckgo.com/l/">A &amp; useful snippet.</a>
<a class="result__a" href="https://example.com/two">Second Result</a>
<a class="result__snippet" href="https://example.com/two">Second snippet.</a>
<a class="result__a" href="javascript:alert(1)">Unsafe</a>
`)
	results, err := parseDuckDuckGoResults(body, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v, want one result", results)
	}
	if results[0].Title != "First Result" || results[0].URL != "https://example.com/one?a=1&b=2" || results[0].Snippet != "A & useful snippet." {
		t.Fatalf("result = %+v", results[0])
	}
}

func TestSearchDuckDuckGoEscapesAndBoundsRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if got := r.Header.Get("User-Agent"); got != webSearchUserAgent {
			t.Errorf("User-Agent = %q, want %q", got, webSearchUserAgent)
		}
		if got := r.URL.Query().Get("q"); got != "query & terms" {
			t.Errorf("q = %q, want escaped query value", got)
		}
		if got := r.URL.Query().Get("df"); got != "w" {
			t.Errorf("df = %q, want w", got)
		}
		fmt.Fprint(w, `<a class="result__a" href="https://example.com/one">One</a><a class="result__snippet">Snippet</a><a class="result__a" href="https://example.com/two">Two</a>`)
	}))
	defer server.Close()

	result, err := searchDuckDuckGo(context.Background(), server.Client(), server.URL, webSearchArguments{
		Query: "query & terms",
		Count: webSearchIntPointer(1),
		Range: "w",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "One") || strings.Contains(result, "Two") {
		t.Fatalf("search result = %q", result)
	}
}

func TestCodexProviderExecutesWebSearchAndSendsFollowUp(t *testing.T) {
	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "latest Go release" {
			t.Errorf("search query = %q", got)
		}
		fmt.Fprint(w, `<a class="result__a" href="https://example.com/go">Go release</a><a class="result__snippet">The latest release.</a>`)
	}))
	defer searchServer.Close()

	var backendRequests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		if len(request.Tools) != 1 {
			t.Errorf("tools = %+v, want one fixed tool", request.Tools)
		} else {
			tool := request.Tools[0]
			if tool.Type != "function" || tool.Name != "web_search" {
				t.Errorf("tool = %+v", tool)
			}
			properties, ok := tool.Parameters["properties"].(map[string]any)
			if !ok {
				t.Errorf("tool properties = %#v", tool.Parameters["properties"])
			} else {
				for _, name := range []string{"query", "count", "range"} {
					if _, ok := properties[name]; !ok {
						t.Errorf("tool properties missing %q: %#v", name, properties)
					}
				}
			}
			required, ok := tool.Parameters["required"].([]any)
			if !ok || len(required) != 1 || required[0] != "query" {
				t.Errorf("tool required = %#v, want [query]", tool.Parameters["required"])
			}
		}

		switch backendRequests.Add(1) {
		case 1:
			if len(request.Input) != 1 || request.Input[0].Content[0].Text != "search now" {
				t.Errorf("initial input = %+v", request.Input)
			}
			fmt.Fprint(w, `{"output":[{"type":"function_call","call_id":"call-1","name":"web_search","arguments":"{\"query\":\"latest Go release\",\"count\":1}"}]}`)
		case 2:
			if len(request.Input) != 3 {
				t.Errorf("follow-up input length = %d, want 3: %+v", len(request.Input), request.Input)
			}
			if len(request.Input) >= 3 {
				if request.Input[1].Type != "function_call" || request.Input[1].CallID != "call-1" || request.Input[1].Name != "web_search" {
					t.Errorf("function call input = %+v", request.Input[1])
				}
				if request.Input[2].Type != "function_call_output" || request.Input[2].CallID != "call-1" || !strings.Contains(request.Input[2].Output, "Go release") || !strings.Contains(request.Input[2].Output, "https://example.com/go") {
					t.Errorf("function output input = %+v", request.Input[2])
				}
			}
			fmt.Fprint(w, `{"output_text":"Final answer after search"}`)
		default:
			t.Errorf("unexpected backend request %d", backendRequests.Load())
			fmt.Fprint(w, `{"output_text":"unexpected"}`)
		}
	}))
	defer backend.Close()

	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := store.Save(Credential{AccessToken: "access-token", AccountID: "account-123", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	provider := NewCodexProvider(store)
	provider.backendURL = backend.URL
	provider.httpClient = backend.Client()
	provider.webSearchURL = searchServer.URL
	provider.webSearchClient = searchServer.Client()

	reply, err := provider.Reply(context.Background(), "gpt-test", "search now")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Final answer after search" {
		t.Fatalf("reply = %q", reply)
	}
	if got := backendRequests.Load(); got != 2 {
		t.Fatalf("backend requests = %d, want 2", got)
	}
}

func webSearchIntPointer(value int) *int {
	return &value
}
