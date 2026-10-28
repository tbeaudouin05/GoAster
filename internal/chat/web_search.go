package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	duckDuckGoHTMLURL       = "https://html.duckduckgo.com/html/"
	webSearchHTTPTimeout    = 10 * time.Second
	webSearchMaxBodyBytes   = 2 << 20
	webSearchDefaultCount   = 10
	webSearchMaxCount       = 10
	webSearchMaxQueryLength = 2048
	webSearchMaxFieldLength = 2000
	webSearchUserAgent      = "GoAster/1.0 (built-in web_search)"
)

var (
	duckDuckGoAnchorPattern    = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a\s*>`)
	duckDuckGoBlockPattern     = regexp.MustCompile(`(?is)<(?:div|span)\b([^>]*)>(.*?)</(?:div|span)\s*>`)
	duckDuckGoAttributePattern = regexp.MustCompile(`(?is)([A-Za-z_:][A-Za-z0-9_.:-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>]+))`)
	duckDuckGoTagPattern       = regexp.MustCompile(`(?is)<[^>]*>`)
	duckDuckGoScriptPattern    = regexp.MustCompile(`(?is)<(?:script|style)\b[^>]*>.*?</(?:script|style)\s*>`)
)

type webSearchArguments struct {
	Query string `json:"query"`
	Count *int   `json:"count,omitempty"`
	Range string `json:"range,omitempty"`
}

type duckDuckGoResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

type duckDuckGoHTMLMatch struct {
	Start int
	End   int
	Attrs map[string]string
	Body  string
}

func newWebSearchHTTPClient() *http.Client {
	return &http.Client{Timeout: webSearchHTTPTimeout}
}

func parseWebSearchArguments(arguments string) (webSearchArguments, error) {
	var args webSearchArguments
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return webSearchArguments{}, fmt.Errorf("web_search arguments are not valid JSON: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err == nil {
		return webSearchArguments{}, fmt.Errorf("web_search arguments contain more than one JSON value")
	} else if err != io.EOF {
		return webSearchArguments{}, fmt.Errorf("web_search arguments are not valid JSON: %w", err)
	}

	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return webSearchArguments{}, fmt.Errorf("web_search query is required")
	}
	if len(args.Query) > webSearchMaxQueryLength {
		return webSearchArguments{}, fmt.Errorf("web_search query exceeds %d characters", webSearchMaxQueryLength)
	}
	if args.Count != nil {
		if *args.Count < 1 {
			return webSearchArguments{}, fmt.Errorf("web_search count must be at least 1")
		}
		if *args.Count > webSearchMaxCount {
			*args.Count = webSearchMaxCount
		}
	}
	if args.Range != "" {
		args.Range = strings.ToLower(strings.TrimSpace(args.Range))
		switch args.Range {
		case "d", "w", "m", "y":
		default:
			return webSearchArguments{}, fmt.Errorf("web_search range must be one of: d, w, m, y")
		}
	}
	return args, nil
}

func searchDuckDuckGo(ctx context.Context, client *http.Client, endpoint string, args webSearchArguments) (string, error) {
	if client == nil {
		client = newWebSearchHTTPClient()
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return "", fmt.Errorf("web_search query is required")
	}
	if len(args.Query) > webSearchMaxQueryLength {
		return "", fmt.Errorf("web_search query exceeds %d characters", webSearchMaxQueryLength)
	}
	count := webSearchDefaultCount
	if args.Count != nil {
		count = *args.Count
	}
	if count < 1 {
		return "", fmt.Errorf("web_search count must be at least 1")
	}
	if count > webSearchMaxCount {
		count = webSearchMaxCount
	}

	rangeCode := strings.ToLower(strings.TrimSpace(args.Range))
	if rangeCode != "" {
		switch rangeCode {
		case "d", "w", "m", "y":
		default:
			return "", fmt.Errorf("web_search range must be one of: d, w, m, y")
		}
	}

	searchURL, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("build DuckDuckGo URL: %w", err)
	}
	if searchURL.Scheme != "http" && searchURL.Scheme != "https" || searchURL.Host == "" {
		return "", fmt.Errorf("build DuckDuckGo URL: endpoint must be an HTTP or HTTPS URL")
	}
	query := searchURL.Query()
	query.Set("q", args.Query)
	if rangeCode == "" {
		query.Del("df")
	} else {
		query.Set("df", duckDuckGoDateFilter(rangeCode))
	}
	searchURL.RawQuery = query.Encode()

	searchContext, cancel := context.WithTimeout(ctx, webSearchHTTPTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(searchContext, http.MethodGet, searchURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("build DuckDuckGo request: %w", err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("User-Agent", webSearchUserAgent)
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("DuckDuckGo request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("DuckDuckGo search failed with status %s", response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, webSearchMaxBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("read DuckDuckGo response: %w", err)
	}
	if len(body) > webSearchMaxBodyBytes {
		return "", fmt.Errorf("DuckDuckGo response exceeds %d bytes", webSearchMaxBodyBytes)
	}
	results, err := parseDuckDuckGoResults(body, count)
	if err != nil {
		return "", fmt.Errorf("parse DuckDuckGo response: %w", err)
	}
	return formatDuckDuckGoResults(args.Query, results), nil
}

func duckDuckGoDateFilter(rangeCode string) string {
	switch rangeCode {
	case "d":
		return "d"
	case "w":
		return "w"
	case "m":
		return "m"
	case "y":
		return "t"
	default:
		return ""
	}
}

func parseDuckDuckGoResults(body []byte, count int) ([]duckDuckGoResult, error) {
	if count <= 0 {
		count = webSearchDefaultCount
	}
	if count > webSearchMaxCount {
		count = webSearchMaxCount
	}

	content := string(body)
	anchors := make([]duckDuckGoHTMLMatch, 0)
	for _, match := range duckDuckGoAnchorPattern.FindAllStringSubmatchIndex(content, -1) {
		if len(match) != 6 {
			continue
		}
		attrs := content[match[2]:match[3]]
		anchors = append(anchors, duckDuckGoHTMLMatch{
			Start: match[0],
			End:   match[1],
			Attrs: parseDuckDuckGoAttributes(attrs),
			Body:  content[match[4]:match[5]],
		})
	}

	resultAnchors := make([]duckDuckGoHTMLMatch, 0)
	snippetMatches := make([]duckDuckGoHTMLMatch, 0)
	for _, anchor := range anchors {
		classes := anchor.Attrs["class"]
		if hasHTMLClass(classes, "result__a") {
			resultAnchors = append(resultAnchors, anchor)
		}
		if hasHTMLClass(classes, "result__snippet") {
			snippetMatches = append(snippetMatches, anchor)
		}
	}

	for _, match := range duckDuckGoBlockPattern.FindAllStringSubmatchIndex(content, -1) {
		if len(match) != 6 {
			continue
		}
		attrs := parseDuckDuckGoAttributes(content[match[2]:match[3]])
		if hasHTMLClass(attrs["class"], "result__snippet") {
			snippetMatches = append(snippetMatches, duckDuckGoHTMLMatch{
				Start: match[0],
				End:   match[1],
				Attrs: attrs,
				Body:  content[match[4]:match[5]],
			})
		}
	}
	sort.SliceStable(snippetMatches, func(i, j int) bool {
		return snippetMatches[i].Start < snippetMatches[j].Start
	})

	results := make([]duckDuckGoResult, 0, minWebSearchInt(count, len(resultAnchors)))
	seenURLs := make(map[string]struct{})
	snippetIndex := 0
	for i, anchor := range resultAnchors {
		if len(results) >= count {
			break
		}
		resultURL, ok := normalizeDuckDuckGoResultURL(anchor.Attrs["href"])
		if !ok {
			continue
		}
		title := cleanDuckDuckGoText(anchor.Body)
		if title == "" {
			continue
		}
		if _, seen := seenURLs[resultURL]; seen {
			continue
		}
		seenURLs[resultURL] = struct{}{}

		boundary := len(content)
		if i+1 < len(resultAnchors) {
			boundary = resultAnchors[i+1].Start
		}
		for snippetIndex < len(snippetMatches) && snippetMatches[snippetIndex].Start < anchor.End {
			snippetIndex++
		}
		snippet := ""
		if snippetIndex < len(snippetMatches) && snippetMatches[snippetIndex].Start < boundary {
			snippet = cleanDuckDuckGoText(snippetMatches[snippetIndex].Body)
			snippetIndex++
		}
		results = append(results, duckDuckGoResult{
			Title:   limitWebSearchField(title),
			URL:     limitWebSearchField(resultURL),
			Snippet: limitWebSearchField(snippet),
		})
	}
	return results, nil
}

func parseDuckDuckGoAttributes(raw string) map[string]string {
	attrs := make(map[string]string)
	for _, match := range duckDuckGoAttributePattern.FindAllStringSubmatch(raw, -1) {
		if len(match) != 5 {
			continue
		}
		value := match[2]
		if value == "" {
			value = match[3]
		}
		if value == "" {
			value = match[4]
		}
		attrs[strings.ToLower(match[1])] = html.UnescapeString(value)
	}
	return attrs
}

func hasHTMLClass(classes, wanted string) bool {
	for _, className := range strings.Fields(classes) {
		if className == wanted {
			return true
		}
	}
	return false
}

func normalizeDuckDuckGoResultURL(raw string) (string, bool) {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if raw == "" {
		return "", false
	}
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	if isDuckDuckGoHost(parsed.Hostname()) {
		redirected := parsed.Query().Get("uddg")
		if redirected == "" {
			return "", false
		}
		redirected = html.UnescapeString(strings.TrimSpace(redirected))
		if strings.HasPrefix(redirected, "//") {
			redirected = "https:" + redirected
		}
		parsed, err = url.Parse(redirected)
		if err != nil || parsed.Host == "" {
			return "", false
		}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	return parsed.String(), true
}

func isDuckDuckGoHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "duckduckgo.com" || strings.HasSuffix(host, ".duckduckgo.com")
}

func cleanDuckDuckGoText(raw string) string {
	cleaned := duckDuckGoScriptPattern.ReplaceAllString(raw, " ")
	cleaned = duckDuckGoTagPattern.ReplaceAllString(cleaned, " ")
	cleaned = html.UnescapeString(cleaned)
	return strings.Join(strings.Fields(cleaned), " ")
}

func limitWebSearchField(value string) string {
	if len(value) <= webSearchMaxFieldLength {
		return value
	}
	return value[:webSearchMaxFieldLength] + "..."
}

func formatDuckDuckGoResults(query string, results []duckDuckGoResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("No results for: %s", query)
	}
	lines := []string{fmt.Sprintf("Results for: %s (via DuckDuckGo)", query)}
	for i, result := range results {
		title := result.Title
		if title == "" {
			title = result.URL
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, title, result.URL))
		if result.Snippet != "" {
			lines = append(lines, "   "+result.Snippet)
		}
	}
	return strings.Join(lines, "\n")
}

func minWebSearchInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
