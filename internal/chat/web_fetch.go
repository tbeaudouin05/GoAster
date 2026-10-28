package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	webFetchTimeout      = 15 * time.Second
	webFetchMaxBodyBytes = 2 << 20
	webFetchMaxRedirects = 5
	webFetchDefaultChars = 20000
	webFetchMaxChars     = 100000
	webFetchMaxURLLength = 8192
	webFetchUserAgent    = "GoAster/1.0 (built-in web_fetch)"
)

var (
	// These special-purpose ranges are not suitable fetch destinations, even
	// when Go considers them global unicast. IPv4-mapped IPv6 is unmapped first.
	webFetchBlockedPrefixes = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("192.88.99.0/24"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("224.0.0.0/4"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("2001::/23"), // Includes Teredo and other special-purpose addresses.
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2002::/16"), // 6to4 can encode an IPv4 destination.
		netip.MustParsePrefix("3ffe::/16"),
		netip.MustParsePrefix("3fff::/20"),
	}
	webFetchIPv6GlobalPrefix = netip.MustParsePrefix("2000::/3")
	webFetchScripts          = regexp.MustCompile(`(?is)<script\b(?:[^>"']|"[^"]*"|'[^']*')*>.*?(?:</script\s*>|$)`)
	webFetchStyles           = regexp.MustCompile(`(?is)<style\b(?:[^>"']|"[^"]*"|'[^']*')*>.*?(?:</style\s*>|$)`)
	webFetchComments         = regexp.MustCompile(`(?is)<!--.*?(?:-->|$)`)
	webFetchTags             = regexp.MustCompile(`(?is)<(?:[^>"']|"[^"]*"|'[^']*')*>`)
)

type webFetchArguments struct {
	URL      string `json:"url"`
	MaxChars *int   `json:"maxChars,omitempty"`
}

type webFetchResult struct {
	URL           string `json:"url"`
	ContentType   string `json:"content_type"`
	Content       string `json:"content"`
	Truncated     bool   `json:"truncated"`
	OriginalChars int64  `json:"original_chars"`
}

func parseWebFetchArguments(arguments string) (webFetchArguments, error) {
	var raw struct {
		URL      string          `json:"url"`
		MaxChars json.RawMessage `json:"maxChars"`
	}
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return webFetchArguments{}, fmt.Errorf("web_fetch arguments are not valid JSON: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err == nil {
		return webFetchArguments{}, fmt.Errorf("web_fetch arguments contain more than one JSON value")
	} else if err != io.EOF {
		return webFetchArguments{}, fmt.Errorf("web_fetch arguments are not valid JSON: %w", err)
	}
	args := webFetchArguments{URL: strings.TrimSpace(raw.URL)}
	if _, err := validateWebFetchURL(args.URL); err != nil {
		return webFetchArguments{}, err
	}
	if raw.MaxChars != nil {
		var maxChars int
		if string(raw.MaxChars) == "null" || json.Unmarshal(raw.MaxChars, &maxChars) != nil || maxChars < 1 || maxChars > webFetchMaxChars {
			return webFetchArguments{}, fmt.Errorf("web_fetch maxChars must be an integer between 1 and %d", webFetchMaxChars)
		}
		args.MaxChars = &maxChars
	}
	return args, nil
}

func validateWebFetchURL(raw string) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > webFetchMaxURLLength {
		return nil, fmt.Errorf("web_fetch URL is required and must not exceed %d bytes", webFetchMaxURLLength)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.Opaque != "" {
		return nil, fmt.Errorf("web_fetch URL must be an absolute HTTP or HTTPS URL")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("web_fetch URL must not contain credentials")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" || strings.Contains(host, "%") || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, fmt.Errorf("web_fetch URL has a prohibited host")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !isPublicWebFetchIP(ip) {
		return nil, fmt.Errorf("web_fetch URL has a prohibited IP address")
	}
	return parsed, nil
}

func isPublicWebFetchIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	// IPv4-embedded and other non-global IPv6 ranges must not tunnel around
	// the IPv4 checks. Only ordinary IPv6 global unicast is permitted.
	if ip.Is6() && !webFetchIPv6GlobalPrefix.Contains(ip) {
		return false
	}
	for _, prefix := range webFetchBlockedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

type webFetchLookupIP func(context.Context, string, string) ([]netip.Addr, error)
type webFetchDial func(context.Context, string, string) (net.Conn, error)

// Resolve and validate all addresses, then dial the validated IP directly.
// This avoids a second DNS lookup between the check and the connection.
func webFetchDialContext(lookup webFetchLookupIP, dial webFetchDial) webFetchDial {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		// The HTTP transport may detach connection setup from a canceled request.
		// Bound DNS and dialing independently as well as the overall request.
		ctx, cancel := context.WithTimeout(ctx, webFetchTimeout)
		defer cancel()
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid web_fetch address: %w", err)
		}
		var addresses []netip.Addr
		if ip, err := netip.ParseAddr(host); err == nil {
			addresses = []netip.Addr{ip}
		} else {
			addresses, err = lookup(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("resolve web_fetch host: %w", err)
			}
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("web_fetch host has no addresses")
		}
		for _, ip := range addresses {
			if !isPublicWebFetchIP(ip) {
				return nil, fmt.Errorf("web_fetch host resolves to a prohibited IP address")
			}
		}
		var lastErr error
		for _, ip := range addresses {
			conn, err := dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, fmt.Errorf("connect to web_fetch host: %w", lastErr)
	}
}

func newWebFetchHTTPClient() *http.Client {
	dialer := &net.Dialer{}
	return &http.Client{
		Timeout: webFetchTimeout,
		// A proxy could resolve or connect to a different address, so do not use
		// environment proxies for this client.
		Transport: &http.Transport{
			DialContext:         webFetchDialContext(net.DefaultResolver.LookupNetIP, dialer.DialContext),
			TLSHandshakeTimeout: webFetchTimeout,
			IdleConnTimeout:     30 * time.Second,
			MaxIdleConns:        10,
		},
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > webFetchMaxRedirects {
				return fmt.Errorf("web_fetch exceeded %d redirects", webFetchMaxRedirects)
			}
			_, err := validateWebFetchURL(request.URL.String())
			return err
		},
	}
}

func fetchWebPage(ctx context.Context, client *http.Client, args webFetchArguments) (string, error) {
	parsed, err := validateWebFetchURL(args.URL)
	if err != nil {
		return "", err
	}
	maxChars := webFetchDefaultChars
	if args.MaxChars != nil {
		maxChars = *args.MaxChars
	}
	if maxChars < 1 || maxChars > webFetchMaxChars {
		return "", fmt.Errorf("web_fetch maxChars must be between 1 and %d", webFetchMaxChars)
	}
	if client == nil {
		client = newWebFetchHTTPClient()
	}
	fetchCtx, cancel := context.WithTimeout(ctx, webFetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", fmt.Errorf("build web_fetch request: %w", err)
	}
	request.Header.Set("User-Agent", webFetchUserAgent)
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("web_fetch request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("web_fetch failed with status %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, webFetchMaxBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("read web_fetch response: %w", err)
	}
	if len(body) > webFetchMaxBodyBytes {
		return "", fmt.Errorf("web_fetch response exceeds %d bytes", webFetchMaxBodyBytes)
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType == "" {
		contentType, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}
	content := string(body)
	originalChars := int64(0)
	formattedJSON := false
	switch {
	case contentType == "text/html" || contentType == "application/xhtml+xml":
		content = extractWebFetchHTML(content)
	case contentType == "application/json" || strings.HasSuffix(contentType, "+json"):
		if json.Valid(body) {
			content, originalChars = formatWebFetchJSON(body, maxChars)
			formattedJSON = true
		}
	}
	if !formattedJSON {
		characters := []rune(content)
		originalChars = int64(len(characters))
		if originalChars > int64(maxChars) {
			content = string(characters[:maxChars])
		}
	}
	result := webFetchResult{
		URL:           response.Request.URL.String(),
		ContentType:   contentType,
		Content:       content,
		Truncated:     originalChars > int64(maxChars),
		OriginalChars: originalChars,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("encode web_fetch result: %w", err)
	}
	return string(encoded), nil
}

func extractWebFetchHTML(content string) string {
	content = webFetchScripts.ReplaceAllString(content, " ")
	content = webFetchStyles.ReplaceAllString(content, " ")
	content = webFetchComments.ReplaceAllString(content, " ")
	content = webFetchTags.ReplaceAllString(content, " ")
	return strings.Join(strings.Fields(html.UnescapeString(content)), " ")
}

// Pretty-print valid JSON while storing only the requested prefix. Deeply
// nested JSON can otherwise expand quadratically when indentation is added.
func formatWebFetchJSON(body []byte, maxChars int) (string, int64) {
	out := webFetchLimitedText{limit: int64(maxChars)}
	depth := 0
	var previous byte
	for i := 0; i < len(body); {
		c := body[i]
		switch c {
		case ' ', '\t', '\r', '\n':
			i++
			continue
		case '"':
			end := i + 1
			for end < len(body) {
				if body[end] == '\\' {
					end += 2
					continue
				}
				if body[end] == '"' {
					end++
					break
				}
				end++
			}
			out.write(string(body[i:end]))
			i = end
		case '{', '[':
			out.write(string(c))
			depth++
			next := i + 1
			for next < len(body) && strings.ContainsRune(" \t\r\n", rune(body[next])) {
				next++
			}
			if next < len(body) && body[next] != '}' && body[next] != ']' {
				out.write("\n")
				out.indent(depth)
			}
			i++
		case '}', ']':
			depth--
			if previous != '{' && previous != '[' {
				out.write("\n")
				out.indent(depth)
			}
			out.write(string(c))
			i++
		case ',':
			out.write(",\n")
			out.indent(depth)
			i++
		case ':':
			out.write(": ")
			i++
		default:
			end := i + 1
			for end < len(body) && !strings.ContainsRune(" \t\r\n{}[],:\"", rune(body[end])) {
				end++
			}
			out.write(string(body[i:end]))
			i = end
		}
		previous = c
	}
	return out.builder.String(), out.length
}

type webFetchLimitedText struct {
	builder strings.Builder
	limit   int64
	length  int64
}

func (out *webFetchLimitedText) write(s string) {
	length := int64(utf8.RuneCountInString(s))
	remaining := out.limit - out.length
	if remaining > 0 {
		if length <= remaining {
			out.builder.WriteString(s)
		} else {
			count := int64(0)
			for i := range s {
				if count == remaining {
					out.builder.WriteString(s[:i])
					break
				}
				count++
			}
		}
	}
	out.length += length
}

func (out *webFetchLimitedText) indent(depth int) {
	length := int64(depth) * 2
	remaining := out.limit - out.length
	if remaining > 0 {
		if remaining < length {
			length = remaining
		}
		out.builder.WriteString(strings.Repeat(" ", int(length)))
	}
	out.length += int64(depth) * 2
}
