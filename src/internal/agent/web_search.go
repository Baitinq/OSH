package agent

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

const (
	duckDuckGoSearchURL  = "https://html.duckduckgo.com/html/"
	defaultSearchResults = 8
	maxSearchResults     = 20
)

var webSearchTool = responses.ToolUnionParam{
	OfFunction: &responses.FunctionToolParam{
		Name:        "web_search",
		Description: openai.String("Search the web for current information using DuckDuckGo and return result titles, URLs, and snippets."),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query.",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum number of results to return (default 8, maximum 20).",
					"minimum":     1,
					"maximum":     maxSearchResults,
				},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
	},
}

type searchResult struct {
	title, url, snippet string
}

var (
	resultLinkRE = regexp.MustCompile(`(?is)<a[^>]+class="[^"]*\bresult__a\b[^"]*"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	snippetRE    = regexp.MustCompile(`(?is)<(?:a|div)[^>]+class="[^"]*\bresult__snippet\b[^"]*"[^>]*>(.*?)</(?:a|div)>`)
	tagRE        = regexp.MustCompile(`(?s)<[^>]*>`)
)

func searchWeb(ctx context.Context, client *http.Client, endpoint, query string, maxResults int) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("query must not be empty")
	}
	if maxResults == 0 {
		maxResults = defaultSearchResults
	}
	if maxResults < 1 || maxResults > maxSearchResults {
		return "", fmt.Errorf("max_results must be between 1 and %d", maxSearchResults)
	}

	form := url.Values{"q": {query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OSH/1.0)")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("DuckDuckGo returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	results := parseDuckDuckGoResults(string(body), maxResults)
	if len(results) == 0 {
		return "No results for: " + query, nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Search results for %q:\n", query)
	for i, result := range results {
		fmt.Fprintf(&out, "\n%d. %s\n   %s", i+1, result.title, result.url)
		if result.snippet != "" {
			fmt.Fprintf(&out, "\n   %s", result.snippet)
		}
	}
	return out.String(), nil
}

func parseDuckDuckGoResults(body string, maxResults int) []searchResult {
	links := resultLinkRE.FindAllStringSubmatch(body, -1)
	snippets := snippetRE.FindAllStringSubmatch(body, -1)
	results := make([]searchResult, 0, min(len(links), maxResults))
	for i, link := range links {
		target := decodeDuckDuckGoURL(html.UnescapeString(link[1]))
		if target == "" {
			continue
		}
		result := searchResult{title: inlineHTMLText(link[2]), url: target}
		if i < len(snippets) {
			result.snippet = inlineHTMLText(snippets[i][1])
		}
		results = append(results, result)
		if len(results) == maxResults {
			break
		}
	}
	return results
}

func decodeDuckDuckGoURL(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if target := u.Query().Get("uddg"); target != "" {
		u, err = url.Parse(target)
		if err != nil {
			return ""
		}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	if strings.EqualFold(u.Hostname(), "duckduckgo.com") && strings.HasPrefix(u.Path, "/y.js") {
		return ""
	}
	return u.String()
}

func inlineHTMLText(value string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tagRE.ReplaceAllString(value, " "))), " ")
}
