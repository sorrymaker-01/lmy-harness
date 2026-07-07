package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestEmbeddingEndpointURL(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{
			name: "api base url",
			base: "https://example.com/api/v3",
			want: "https://example.com/api/v3/embeddings",
		},
		{
			name: "standard embeddings endpoint",
			base: "https://example.com/api/v3/embeddings",
			want: "https://example.com/api/v3/embeddings",
		},
		{
			name: "multimodal embeddings endpoint",
			base: "https://example.com/api/v3/embeddings/multimodal",
			want: "https://example.com/api/v3/embeddings/multimodal",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := embeddingEndpointURL(tt.base); got != tt.want {
				t.Fatalf("embeddingEndpointURL(%q) = %q, want %q", tt.base, got, tt.want)
			}
		})
	}
}

func TestChatEndpointURL(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{
			name: "api base url",
			base: "https://api.deepseek.com",
			want: "https://api.deepseek.com/chat/completions",
		},
		{
			name: "full chat completions endpoint",
			base: "https://api.deepseek.com/chat/completions",
			want: "https://api.deepseek.com/chat/completions",
		},
		{
			name: "trailing slash",
			base: "https://api.deepseek.com/",
			want: "https://api.deepseek.com/chat/completions",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chatEndpointURL(tt.base); got != tt.want {
				t.Fatalf("chatEndpointURL(%q) = %q, want %q", tt.base, got, tt.want)
			}
		})
	}
}

func TestOpenAIEmbeddingClientStandardBatch(t *testing.T) {
	client := NewOpenAIEmbeddingClient(Config{
		APIKey:         "test-key",
		BaseURL:        "https://example.com",
		Model:          "embed-model",
		EmbeddingModel: "embed-model",
	})
	client.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q, want /embeddings", r.URL.Path)
		}
		var payload struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, err
		}
		if payload.Model != "embed-model" {
			t.Errorf("model = %q, want embed-model", payload.Model)
		}
		if len(payload.Input) != 2 || payload.Input[0] != "hello" || payload.Input[1] != "world" {
			t.Errorf("input = %#v, want text batch", payload.Input)
		}
		return embeddingTestResponse(`{"data":[{"index":0,"embedding":[1,2]},{"index":1,"embedding":[3,4]}]}`), nil
	})}
	vectors, err := client.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 2 || len(vectors[0]) != 2 || vectors[0][0] != 1 || vectors[1][0] != 3 {
		t.Fatalf("vectors = %#v, want two parsed vectors", vectors)
	}
}

func TestOpenAIEmbeddingClientMultimodalTextRequests(t *testing.T) {
	requests := []string{}
	client := NewOpenAIEmbeddingClient(Config{
		APIKey:         "test-key",
		BaseURL:        "https://example.com/api/v3/embeddings/multimodal",
		Model:          "embed-model",
		EmbeddingModel: "embed-model",
	})
	client.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v3/embeddings/multimodal" {
			t.Errorf("path = %q, want /api/v3/embeddings/multimodal", r.URL.Path)
		}
		var payload struct {
			Model string `json:"model"`
			Input []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, err
		}
		if payload.Model != "embed-model" {
			t.Errorf("model = %q, want embed-model", payload.Model)
		}
		if len(payload.Input) != 1 || payload.Input[0].Type != "text" {
			t.Errorf("input = %#v, want one text content block", payload.Input)
		}
		requests = append(requests, payload.Input[0].Text)
		if len(requests) == 1 {
			return embeddingTestResponse(`{"data":{"object":"embedding","embedding":[1,2,3]}}`), nil
		}
		return embeddingTestResponse(`{"data":{"object":"embedding","embedding":[4,5,6]}}`), nil
	})}
	vectors, err := client.Embed(context.Background(), []string{"天很蓝", "海很深"})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0] != "天很蓝" || requests[1] != "海很深" {
		t.Fatalf("requests = %#v, want one request per text", requests)
	}
	if len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][0] != 4 {
		t.Fatalf("vectors = %#v, want two parsed vectors", vectors)
	}
}

func TestParseEmbeddingResponseRejectsSingleVectorForBatch(t *testing.T) {
	_, err := parseEmbeddingResponse([]byte(`{"data":{"embedding":[1,2]}}`), 2)
	if err == nil {
		t.Fatal("expected error")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func embeddingTestResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"content-type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
