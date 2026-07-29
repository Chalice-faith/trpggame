package ai_client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"trpggame/internal/config"
)

func TestClientUsesInternalSecretForScriptOperations(t *testing.T) {
	var parseCalled bool
	var deleteCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Secret") != "test-secret" {
			t.Errorf("internal secret = %q", r.Header.Get("X-Internal-Secret"))
		}

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/ai/parse-script":
			parseCalled = true
			var request ParseScriptRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode parse request: %v", err)
			}
			if request.ScriptID != 42 {
				t.Errorf("parse script ID = %d", request.ScriptID)
			}
			_ = json.NewEncoder(w).Encode(ParseScriptResponse{
				Success: true,
				Message: "accepted",
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/ai/scripts/42/vectors":
			deleteCalled = true
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(
		&config.AIConfig{BaseURL: server.URL, Timeout: 5},
		"test-secret",
	)

	result, err := client.ParseScript(context.Background(), &ParseScriptRequest{
		ScriptID: 42,
		FilePath: "scripts/7/42/file.pdf",
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("ParseScript() = (%#v, %v)", result, err)
	}
	if err := client.DeleteScriptVectors(context.Background(), 42); err != nil {
		t.Fatalf("DeleteScriptVectors() error = %v", err)
	}
	if !parseCalled || !deleteCalled {
		t.Fatalf("calls = (parse=%v, delete=%v)", parseCalled, deleteCalled)
	}
}

func TestClientDeleteScriptVectorsReportsRemoteFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Milvus unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := NewClient(
		&config.AIConfig{BaseURL: server.URL, Timeout: 5},
		"test-secret",
	)

	if err := client.DeleteScriptVectors(context.Background(), 42); err == nil {
		t.Fatal("expected remote cleanup failure")
	}
}
