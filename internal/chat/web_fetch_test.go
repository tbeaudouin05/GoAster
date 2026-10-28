package chat

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseWebFetchArguments(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{"valid", `{"url":" https://example.com/path ","maxChars":32}`, ""},
		{"default limit", `{"url":"http://example.com"}`, ""},
		{"missing URL", `{}`, "URL"},
		{"null", `null`, "URL"},
		{"malformed", `{`, "JSON"},
		{"array", `[]`, "JSON"},
		{"unknown field", `{"url":"https://example.com","extra":true}`, "JSON"},
		{"multiple values", `{"url":"https://example.com"} {}`, "more than one"},
		{"relative", `{"url":"/page"}`, "HTTP or HTTPS"},
		{"file scheme", `{"url":"file:///etc/passwd"}`, "HTTP or HTTPS"},
		{"ftp scheme", `{"url":"ftp://example.com"}`, "HTTP or HTTPS"},
		{"credentials", `{"url":"https://user:password@example.com"}`, "credentials"},
		{"localhost", `{"url":"http://localhost"}`, "prohibited"},
		{"localhost subdomain", `{"url":"http://api.LOCALHOST."}`, "prohibited"},
		{"private IP", `{"url":"http://10.0.0.1"}`, "prohibited"},
		{"IPv6 zone", `{"url":"http://[fe80::1%25eth0]"}`, "prohibited"},
		{"zero limit", `{"url":"https://example.com","maxChars":0}`, "maxChars"},
		{"negative limit", `{"url":"https://example.com","maxChars":-1}`, "maxChars"},
		{"excessive limit", `{"url":"https://example.com","maxChars":100001}`, "maxChars"},
		{"fractional limit", `{"url":"https://example.com","maxChars":1.5}`, "maxChars"},
		{"limit type", `{"url":"https://example.com","maxChars":"50"}`, "maxChars"},
		{"null limit", `{"url":"https://example.com","maxChars":null}`, "maxChars"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args, err := parseWebFetchArguments(test.args)
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("error = %v, want %q", err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(args.URL) != args.URL {
				t.Fatalf("URL was not trimmed: %q", args.URL)
			}
		})
	}
	_, err := parseWebFetchArguments(fmt.Sprintf(`{"url":"https://example.com/%s"}`, strings.Repeat("a", webFetchMaxURLLength)))
	if err == nil || !strings.Contains(err.Error(), "URL") {
		t.Fatalf("overlong URL error = %v", err)
	}
}

func TestExtractWebFetchHTML(t *testing.T) {
	page := `<!DOCTYPE html><html><head><style>body { color: red }</style>
<script type="text/javascript">const hidden = "</style>";</script></head>
<body><!-- hidden comment --><h1> Hello&nbsp;world </h1><p title="1 > 0">A &amp; <b> B </b></p>
<SCRIPT>ignored()</SCRIPT><style>also ignored</style></body></html>`
	if got, want := extractWebFetchHTML(page), "Hello world A & B"; got != want {
		t.Fatalf("extracted = %q, want %q", got, want)
	}
	if got := extractWebFetchHTML("<p>Visible</p><script>unfinished hidden"); got != "Visible" {
		t.Fatalf("unclosed script extraction = %q", got)
	}
}

func TestFormatWebFetchJSON(t *testing.T) {
	inputs := []string{
		`{"message":"héllo \\\"world\\\"","items":[{},[],true,null,-3.4e+2]}`,
		` [ { "a" : [ 1, 2, { "b" : "🙂" } ] }, " spaced text " ] `,
		`{"empty":{},"nested":[[],{}]}`,
		`"single value"`,
	}
	for _, input := range inputs {
		var expected bytes.Buffer
		if err := json.Indent(&expected, []byte(strings.TrimSpace(input)), "", "  "); err != nil {
			t.Fatalf("invalid test JSON %q: %v", input, err)
		}
		characters := []rune(expected.String())
		for _, limit := range []int{1, 5, len(characters) - 1, len(characters), len(characters) + 1} {
			got, length := formatWebFetchJSON([]byte(input), limit)
			wantLength := limit
			if wantLength > len(characters) {
				wantLength = len(characters)
			}
			if got != string(characters[:wantLength]) || length != int64(len(characters)) {
				t.Errorf("input %q limit %d: output = %q, length = %d; want %q, %d", input, limit, got, length, string(characters[:wantLength]), len(characters))
			}
		}
	}
}

func TestFetchWebPageContentTypesAndTruncation(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("User-Agent") != webFetchUserAgent {
			t.Errorf("request method = %q, user agent = %q", r.Method, r.Header.Get("User-Agent"))
		}
		switch r.URL.Path {
		case "/html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<h1>Hello &amp; welcome</h1><script>hidden</script><p>Next</p>`)
		case "/json":
			w.Header().Set("Content-Type", "application/problem+json")
			fmt.Fprint(w, `{"answer":42,"items":[1,2]}`)
		case "/invalid-json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{invalid`)
		case "/text":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprint(w, "a🙂b c")
		case "/default":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, strings.Repeat("x", webFetchDefaultChars+1))
		case "/deep-json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, strings.Repeat("[", 5000)+"0"+strings.Repeat("]", 5000))
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	client := webFetchTestClient(t, server, nil)

	tests := []struct {
		path          string
		maxChars      *int
		contentType   string
		content       string
		originalChars int64
		truncated     bool
	}{
		{"/html", nil, "text/html", "Hello & welcome Next", 20, false},
		{"/json", nil, "application/problem+json", "{\n  \"answer\": 42,\n  \"items\": [\n    1,\n    2\n  ]\n}", 49, false},
		{"/invalid-json", nil, "application/json", "{invalid", 8, false},
		{"/text", webFetchIntPointer(4), "text/plain", "a🙂b ", 5, true},
		{"/default", nil, "text/plain", strings.Repeat("x", webFetchDefaultChars), webFetchDefaultChars + 1, true},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			result := mustFetchWebPage(t, client, webFetchArguments{URL: "http://public.test" + test.path, MaxChars: test.maxChars})
			if result.URL != "http://public.test"+test.path || result.ContentType != test.contentType || result.Content != test.content || result.OriginalChars != test.originalChars || result.Truncated != test.truncated {
				t.Fatalf("result = %+v", result)
			}
		})
	}
	deep := mustFetchWebPage(t, client, webFetchArguments{URL: "http://public.test/deep-json", MaxChars: webFetchIntPointer(32)})
	if len([]rune(deep.Content)) != 32 || !deep.Truncated || deep.OriginalChars <= webFetchMaxBodyBytes || !strings.HasPrefix(deep.Content, "[\n  [\n    [") {
		t.Fatalf("deep JSON result = %+v", deep)
	}
}

func TestFetchWebPageResponseSizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		switch r.URL.Path {
		case "/limit":
			fmt.Fprint(w, strings.Repeat("x", webFetchMaxBodyBytes))
		case "/oversize":
			fmt.Fprint(w, strings.Repeat("x", webFetchMaxBodyBytes+1))
		case "/compressed":
			w.Header().Set("Content-Encoding", "gzip")
			writer := gzip.NewWriter(w)
			_, _ = writer.Write([]byte(strings.Repeat("x", webFetchMaxBodyBytes+1)))
			_ = writer.Close()
		}
	}))
	defer server.Close()
	client := webFetchTestClient(t, server, nil)
	result := mustFetchWebPage(t, client, webFetchArguments{URL: "http://public.test/limit", MaxChars: webFetchIntPointer(3)})
	if result.Content != "xxx" || !result.Truncated || result.OriginalChars != webFetchMaxBodyBytes {
		t.Fatalf("boundary result = %+v", result)
	}
	for _, path := range []string{"/oversize", "/compressed"} {
		_, err := fetchWebPage(context.Background(), client, webFetchArguments{URL: "http://public.test" + path})
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("%s error = %v, want response size error", path, err)
		}
	}
}

func TestWebFetchPublicAddressChecks(t *testing.T) {
	tests := []struct {
		ip     string
		public bool
	}{
		{"8.8.8.8", true}, {"2606:4700:4700::1111", true}, {"::ffff:8.8.8.8", true},
		{"0.0.0.0", false}, {"10.1.2.3", false}, {"100.100.100.200", false},
		{"127.0.0.1", false}, {"169.254.169.254", false}, {"172.16.0.1", false},
		{"192.168.1.1", false}, {"192.0.0.1", false}, {"192.0.2.1", false},
		{"198.18.0.1", false}, {"198.51.100.1", false}, {"203.0.113.1", false},
		{"224.0.0.1", false}, {"240.0.0.1", false}, {"255.255.255.255", false},
		{"::1", false}, {"::ffff:127.0.0.1", false}, {"fd00:ec2::254", false},
		{"fe80::1", false}, {"2606:4700:4700::1111%eth0", false}, {"2001:db8::1", false}, {"2001::1", false},
		{"2002:0a00:0001::1", false}, {"64:ff9b::a00:1", false}, {"3fff::1", false},
	}
	for _, test := range tests {
		t.Run(test.ip, func(t *testing.T) {
			if got := isPublicWebFetchIP(netip.MustParseAddr(test.ip)); got != test.public {
				t.Errorf("public(%s) = %t, want %t", test.ip, got, test.public)
			}
		})
	}
}

func TestWebFetchDialValidatesAndPinsDNS(t *testing.T) {
	var lookups, dials int
	lookup := func(_ context.Context, _, _ string) ([]netip.Addr, error) {
		lookups++
		if lookups > 1 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	client, peer := net.Pipe()
	defer peer.Close()
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		dials++
		if address != "8.8.8.8:80" {
			t.Errorf("dialed %q, want validated IP", address)
		}
		return client, nil
	}
	conn, err := webFetchDialContext(lookup, dial)(context.Background(), "tcp", "rebind.test:80")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if lookups != 1 || dials != 1 {
		t.Fatalf("lookups = %d, dials = %d", lookups, dials)
	}

	for _, addresses := range [][]netip.Addr{
		{netip.MustParseAddr("169.254.169.254")},
		{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")},
		nil,
	} {
		dials = 0
		lookup := func(context.Context, string, string) ([]netip.Addr, error) { return addresses, nil }
		if _, err := webFetchDialContext(lookup, dial)(context.Background(), "tcp", "private.test:80"); err == nil || dials != 0 {
			t.Errorf("addresses = %v, error = %v, dials = %d", addresses, err, dials)
		}
	}
}

func TestWebFetchRedirectProtection(t *testing.T) {
	var blockedRequests atomic.Int32
	var redirectRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/private-ip":
			http.Redirect(w, r, "http://127.0.0.1/blocked", http.StatusFound)
		case "/localhost":
			http.Redirect(w, r, "http://localhost/blocked", http.StatusFound)
		case "/private-dns":
			http.Redirect(w, r, "http://private.test/blocked", http.StatusFound)
		case "/file":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case "/public":
			http.Redirect(w, r, "http://other.test/final", http.StatusFound)
		case "/loop":
			redirectRequests.Add(1)
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/blocked":
			blockedRequests.Add(1)
		case "/final":
			fmt.Fprint(w, "safe redirect")
		}
	}))
	defer server.Close()
	client := webFetchTestClient(t, server, map[string][]netip.Addr{
		"private.test": {netip.MustParseAddr("10.1.2.3")},
	})
	for _, path := range []string{"/private-ip", "/localhost", "/private-dns", "/file"} {
		_, err := fetchWebPage(context.Background(), client, webFetchArguments{URL: "http://public.test" + path})
		if err == nil {
			t.Errorf("%s redirect unexpectedly succeeded", path)
		}
	}
	if got := blockedRequests.Load(); got != 0 {
		t.Fatalf("blocked destination received %d requests", got)
	}
	result := mustFetchWebPage(t, client, webFetchArguments{URL: "http://public.test/public"})
	if result.URL != "http://other.test/final" || result.Content != "safe redirect" {
		t.Fatalf("public redirect result = %+v", result)
	}
	_, err := fetchWebPage(context.Background(), client, webFetchArguments{URL: "http://public.test/loop"})
	if err == nil || !strings.Contains(err.Error(), "redirects") || redirectRequests.Load() != webFetchMaxRedirects+1 {
		t.Fatalf("redirect limit error = %v, requests = %d", err, redirectRequests.Load())
	}
}

func TestWebFetchHonorsContextTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client := webFetchTestClient(t, server, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := fetchWebPage(ctx, client, webFetchArguments{URL: "http://public.test/"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestCodexProviderSearchesFetchesAndContinues(t *testing.T) {
	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a class="result__a" href="http://public.test/article">Chosen article</a>`)
	}))
	defer searchServer.Close()
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<h1>Article</h1><p>Evidence from the page.</p><script>hidden</script>`)
	}))
	defer pageServer.Close()

	var backendRequests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		var search, fetch bool
		for _, tool := range request.Tools {
			if tool.Name == "web_search" && tool.Type == "function" {
				search = true
			}
			if tool.Name == "web_fetch" && tool.Type == "function" {
				fetch = true
				properties, ok := tool.Parameters["properties"].(map[string]any)
				if !ok || properties["url"] == nil || properties["maxChars"] == nil {
					t.Errorf("fetch tool properties = %#v", tool.Parameters["properties"])
				}
				required, ok := tool.Parameters["required"].([]any)
				if !ok || len(required) != 1 || required[0] != "url" {
					t.Errorf("fetch tool required = %#v", tool.Parameters["required"])
				}
			}
		}
		if !search || !fetch {
			t.Errorf("missing search or fetch tool: %+v", request.Tools)
		}
		switch backendRequests.Add(1) {
		case 1:
			writeCodexFunctionCall(t, w, "search-1", "web_search", `{"query":"article"}`)
		case 2:
			if len(request.Input) != 3 || request.Input[1].Name != "web_search" || request.Input[2].CallID != "search-1" || !strings.Contains(request.Input[2].Output, "http://public.test/article") {
				t.Errorf("search follow-up = %+v", request.Input)
			}
			writeCodexFunctionCall(t, w, "fetch-1", "web_fetch", `{"url":"http://public.test/article","maxChars":100}`)
		case 3:
			if len(request.Input) != 5 {
				t.Errorf("fetch follow-up input length = %d", len(request.Input))
			} else {
				call, output := request.Input[3], request.Input[4]
				if call.Type != "function_call" || call.Name != "web_fetch" || call.CallID != "fetch-1" || output.Type != "function_call_output" || output.CallID != "fetch-1" {
					t.Errorf("fetch call and output = %+v, %+v", call, output)
				}
				var result webFetchResult
				if err := json.Unmarshal([]byte(output.Output), &result); err != nil || result.Content != "Article Evidence from the page." || result.Truncated {
					t.Errorf("fetch output = %+v, error = %v", result, err)
				}
			}
			fmt.Fprint(w, `{"output_text":"Final answer based on the article"}`)
		default:
			t.Errorf("unexpected backend request")
		}
	}))
	defer backend.Close()
	provider := newShellTestProvider(t, backend)
	provider.webSearchURL = searchServer.URL
	provider.webSearchClient = searchServer.Client()
	provider.webFetchClient = webFetchTestClient(t, pageServer, nil)
	reply, err := provider.Reply(context.Background(), "gpt-test", "find and read the article")
	if err != nil || reply != "Final answer based on the article" || backendRequests.Load() != 3 {
		t.Fatalf("reply = %q, error = %v, requests = %d", reply, err, backendRequests.Load())
	}
}

func TestCodexProviderReturnsFetchErrorToModel(t *testing.T) {
	var backendRequests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		switch backendRequests.Add(1) {
		case 1:
			writeCodexFunctionCall(t, w, "fetch-bad", "web_fetch", `{"url":"http://127.0.0.1/"}`)
		case 2:
			if len(request.Input) != 3 || request.Input[2].CallID != "fetch-bad" || !strings.Contains(request.Input[2].Output, "prohibited") {
				t.Errorf("error follow-up = %+v", request.Input)
			}
			fmt.Fprint(w, `{"output_text":"Could not fetch the page"}`)
		default:
			t.Errorf("unexpected backend request")
		}
	}))
	defer backend.Close()
	provider := newShellTestProvider(t, backend)
	reply, err := provider.Reply(context.Background(), "gpt-test", "fetch this")
	if err != nil || reply != "Could not fetch the page" || backendRequests.Load() != 2 {
		t.Fatalf("reply = %q, error = %v, requests = %d", reply, err, backendRequests.Load())
	}
}

func webFetchTestClient(t *testing.T, server *httptest.Server, addresses map[string][]netip.Addr) *http.Client {
	t.Helper()
	client := newWebFetchHTTPClient()
	transport := client.Transport.(*http.Transport)
	lookup := func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if result, ok := addresses[host]; ok {
			return result, nil
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	dialer := &net.Dialer{}
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, server.Listener.Addr().String())
	}
	transport.DialContext = webFetchDialContext(lookup, dial)
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func mustFetchWebPage(t *testing.T, client *http.Client, args webFetchArguments) webFetchResult {
	t.Helper()
	output, err := fetchWebPage(context.Background(), client, args)
	if err != nil {
		t.Fatal(err)
	}
	var result webFetchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode web_fetch result: %v", err)
	}
	return result
}

func webFetchIntPointer(value int) *int {
	return &value
}
