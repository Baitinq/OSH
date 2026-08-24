package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSearchWebReturnsDuckDuckGoResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.FormValue("q"); got != "openai responses" {
			t.Errorf("query = %q", got)
		}
		if got := r.Header.Get("User-Agent"); !strings.Contains(got, "OSH") {
			t.Errorf("User-Agent = %q", got)
		}
		fmt.Fprint(w, `<html><body>
<div class="result results_links result--ad"><h2><a class="result__a" href="https://duckduckgo.com/y.js?ad_provider=bing">Ad</a></h2><a class="result__snippet">Buy now</a></div>
<div class="result results_links web-result"><h2><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fdocs&amp;rut=x">Example &amp; Docs</a></h2><a class="result__snippet">The <b>official</b> documentation.</a></div>
<div class="result results_links web-result"><h2><a class="result__a" href="https://example.org/news">Latest news</a></h2><div class="result__snippet">Released today.</div></div>
</body></html>`)
	}))
	defer server.Close()

	output, err := searchWeb(t.Context(), server.Client(), server.URL, " openai responses ", 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Search results for "openai responses":`,
		"1. Example & Docs",
		"https://example.com/docs",
		"The official documentation.",
		"2. Latest news",
		"https://example.org/news",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output does not contain %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "Buy now") || strings.Contains(output, "duckduckgo.com/y.js") {
		t.Fatalf("output contains an ad:\n%s", output)
	}
}

func TestSearchWebValidatesArgumentsAndHTTPStatus(t *testing.T) {
	if _, err := searchWeb(t.Context(), http.DefaultClient, "unused", " ", 8); err == nil {
		t.Fatal("empty query was accepted")
	}
	if _, err := searchWeb(t.Context(), http.DefaultClient, "unused", "test", maxSearchResults+1); err == nil {
		t.Fatal("excessive result count was accepted")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "blocked", http.StatusTooManyRequests)
	}))
	defer server.Close()
	if _, err := searchWeb(t.Context(), server.Client(), server.URL, "test", 1); err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("status error = %v", err)
	}
}

func TestDecodeDuckDuckGoURL(t *testing.T) {
	target := "https://example.com/a path?q=one"
	wrapped := "https://duckduckgo.com/l/?uddg=" + url.QueryEscape(target) + "&rut=test"
	if got := decodeDuckDuckGoURL(wrapped); got != "https://example.com/a%20path?q=one" {
		t.Fatalf("decoded URL = %q", got)
	}
	if got := decodeDuckDuckGoURL("javascript:alert(1)"); got != "" {
		t.Fatalf("unsafe URL = %q", got)
	}
}
