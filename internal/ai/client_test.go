package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockLLM returns an httptest server that mimics an OpenAI-compatible /chat/completions
// endpoint, replying with the given assistant content.
func mockLLM(t *testing.T, content string, assertAuth string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if assertAuth != "" && r.Header.Get("Authorization") != assertAuth {
			t.Errorf("Authorization = %q, want %q", r.Header.Get("Authorization"), assertAuth)
		}
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": content}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestOpenAIClientComplete(t *testing.T) {
	srv := mockLLM(t, "hello world", "Bearer secret-key")
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "secret-key", "llama3", 5*time.Second)
	out, err := c.Complete(context.Background(), "system", "user")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out != "hello world" {
		t.Errorf("content = %q", out)
	}
}

func TestDiagnoseThroughHTTP(t *testing.T) {
	srv := mockLLM(t, goodJSON, "") // goodJSON defined in ai_test.go (same package)
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "", "llama3", 5*time.Second)
	p, err := Diagnose(context.Background(), c,
		Incident{SourceEventID: "e1", Kind: "rejection_spike", Title: "spike at microsoft"}, "llama3")
	if err != nil {
		t.Fatalf("Diagnose over HTTP: %v", err)
	}
	if p.Kind != "rca" || p.Narrative == "" || len(p.Recommendations) != 1 {
		t.Errorf("unexpected payload: %+v", p)
	}
}

func TestOpenAIClientErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "", "llama3", 5*time.Second)
	if _, err := c.Complete(context.Background(), "s", "u"); err == nil {
		t.Error("expected error on 500 response")
	}
}
