package tools

import (
	"context"
	"strings"
	"testing"

	"code.byted.org/ai/lmy/apps/server/internal/runtime"
)

func TestRegisterInteractiveWeb(t *testing.T) {
	registry := runtime.NewRuntime()
	RegisterInteractiveWeb(registry)

	names := map[string]bool{}
	for _, tool := range registry.ListTools() {
		names[tool.Name] = true
	}
	for _, name := range []string{"AskUserQuestion", "WebFetch", "WebSearch"} {
		if !names[name] {
			t.Fatalf("expected %s to be registered", name)
		}
	}
}

func TestAskUserQuestionTool(t *testing.T) {
	output, err := NewAskUserQuestionTool().Invoke(context.Background(), map[string]any{
		"question": "Which environment should I use?",
		"choices":  []any{"dev", "prod"},
	}, runtime.InvocationContext{})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	body, ok := output.(map[string]any)
	if !ok {
		t.Fatalf("expected map output, got %T", output)
	}
	if body["status"] != "waiting_for_user" {
		t.Fatalf("unexpected status: %v", body["status"])
	}
	choices, ok := body["choices"].([]string)
	if !ok || len(choices) != 2 {
		t.Fatalf("unexpected choices: %#v", body["choices"])
	}
}

func TestValidatePublicHTTPURLBlocksLocalhost(t *testing.T) {
	for _, rawURL := range []string{
		"http://localhost:3000",
		"http://127.0.0.1:3000",
		"http://10.0.0.1",
	} {
		if _, err := validatePublicHTTPURL(context.Background(), rawURL); err == nil {
			t.Fatalf("expected %s to be blocked", rawURL)
		}
	}
}

func TestHTMLToTextAndTitle(t *testing.T) {
	page := `<html><head><title>Example &amp; Test</title><style>.x{}</style></head><body><h1>Hello</h1><script>alert(1)</script><p>World&nbsp;now</p></body></html>`
	if got := htmlTitle(page); got != "Example & Test" {
		t.Fatalf("unexpected title: %q", got)
	}
	text := htmlToText(page)
	for _, want := range []string{"Hello", "World now"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
	if strings.Contains(text, "alert") || strings.Contains(text, ".x") {
		t.Fatalf("script/style content leaked into text: %q", text)
	}
}

func TestParseDuckDuckGoResults(t *testing.T) {
	page := `<div class="result">
<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fdoc">Example <b>Doc</b></a>
<a class="result__snippet">A useful &amp; short snippet.</a>
</div>`
	results := parseDuckDuckGoResults(page, 5)
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].Title != "Example Doc" {
		t.Fatalf("unexpected title: %q", results[0].Title)
	}
	if results[0].URL != "https://example.com/doc" {
		t.Fatalf("unexpected url: %q", results[0].URL)
	}
	if results[0].Snippet != "A useful & short snippet." {
		t.Fatalf("unexpected snippet: %q", results[0].Snippet)
	}
}
