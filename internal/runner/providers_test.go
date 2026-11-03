package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOpenAIProviderSpeaksChatCompletions(t *testing.T) {
	dir := t.TempDir()

	var mu sync.Mutex
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q, want the configured key", got)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		requests = append(requests, body)
		count := len(requests)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if count == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{
					"role":    "assistant",
					"content": "",
					"tool_calls": []any{map[string]any{
						"id":   "call-1",
						"type": "function",
						"function": map[string]any{
							"name":      "write_file",
							"arguments": `{"path":"out.txt","content":"hi"}`,
						},
					}},
				}}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "Wrote out.txt.",
			}}},
		})
	}))
	defer server.Close()

	provider, err := NewProvider(ModelConfig{
		Provider:  ProviderOpenAI,
		Model:     "llama3.1",
		BaseURL:   server.URL + "/v1",
		APIKey:    "sk-test",
		Workspace: dir,
		MaxSteps:  4,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	var events []Event
	result := provider.Execute(context.Background(),
		Run{TaskDocument: `{"item":{"key":"TT-1","title":"Write a file"}}`},
		func(event Event) { events = append(events, event) })

	if result.Outcome != "succeeded" || result.Summary != "Wrote out.txt." {
		t.Fatalf("result = %+v, want the model's summary", result)
	}
	content, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil || string(content) != "hi" {
		t.Fatalf("out.txt = %q (%v), want the tool's write", content, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want the tool turn and the final answer", len(requests))
	}

	first := requests[0]
	if first["model"] != "llama3.1" {
		t.Errorf("model = %v, want llama3.1", first["model"])
	}
	tools, ok := first["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools = %v, want the declarations", first["tools"])
	}
	if kind := tools[0].(map[string]any)["type"]; kind != "function" {
		t.Errorf("tool type = %v, want function", kind)
	}

	messages, ok := first["messages"].([]any)
	if !ok || len(messages) < 2 {
		t.Fatalf("messages = %v, want a system prompt and the task", first["messages"])
	}
	if role := messages[0].(map[string]any)["role"]; role != "system" {
		t.Errorf("first role = %v, want system", role)
	}

	second, ok := requests[1]["messages"].([]any)
	if !ok || len(second) == 0 {
		t.Fatalf("second request has no messages")
	}
	last := second[len(second)-1].(map[string]any)
	if last["role"] != "tool" || last["tool_call_id"] != "call-1" {
		t.Errorf("last message = %v, want the tool result for call-1", last)
	}
}

func TestAnthropicProviderGroupsToolResults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var mu sync.Mutex
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %q, want /v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "key-test" {
			t.Errorf("x-api-key = %q, want the configured key", got)
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("anthropic-version header is missing")
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		requests = append(requests, body)
		count := len(requests)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if count == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": []any{
					map[string]any{"type": "tool_use", "id": "tu-1", "name": "read_file", "input": map[string]any{"path": "a.txt"}},
					map[string]any{"type": "tool_use", "id": "tu-2", "name": "list_files", "input": map[string]any{"path": "."}},
				},
				"stop_reason": "tool_use",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content":     []any{map[string]any{"type": "text", "text": "The file says alpha."}},
			"stop_reason": "end_turn",
		})
	}))
	defer server.Close()

	provider, err := NewProvider(ModelConfig{
		Provider:  ProviderAnthropic,
		Model:     "claude-test",
		BaseURL:   server.URL,
		APIKey:    "key-test",
		Workspace: dir,
		MaxSteps:  4,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	result := provider.Execute(context.Background(), Run{}, func(Event) {})
	if result.Outcome != "succeeded" || !strings.Contains(result.Summary, "alpha") {
		t.Fatalf("result = %+v, want the model's summary", result)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want two turns", len(requests))
	}

	first := requests[0]
	if first["system"] == "" {
		t.Error("the system prompt was not sent as a top-level field")
	}
	tools, ok := first["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools = %v, want the declarations", first["tools"])
	}
	if _, ok := tools[0].(map[string]any)["input_schema"]; !ok {
		t.Errorf("tool = %v, want input_schema", tools[0])
	}

	second, ok := requests[1]["messages"].([]any)
	if !ok || len(second) < 2 {
		t.Fatalf("messages = %v, want the assistant turn and the results", requests[1]["messages"])
	}
	last := second[len(second)-1].(map[string]any)
	if last["role"] != "user" {
		t.Errorf("tool results role = %v, want user", last["role"])
	}
	blocks, ok := last["content"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("blocks = %v, want both results in one user message", last["content"])
	}
	ids := map[string]bool{}
	for _, block := range blocks {
		entry := block.(map[string]any)
		if entry["type"] != "tool_result" {
			t.Errorf("block type = %v, want tool_result", entry["type"])
		}
		ids[entry["tool_use_id"].(string)] = true
	}
	if !ids["tu-1"] || !ids["tu-2"] {
		t.Errorf("tool_use_ids = %v, want tu-1 and tu-2", ids)
	}
}

func TestNewProviderRejectsIncompleteConfigurations(t *testing.T) {
	cases := []struct {
		name string
		cfg  ModelConfig
		want string
	}{
		{"unknown", ModelConfig{Provider: "gpt"}, "unknown provider"},
		{"openai without model", ModelConfig{Provider: ProviderOpenAI}, "needs a model"},
		{"anthropic without model", ModelConfig{Provider: ProviderAnthropic, APIKey: "k"}, "needs a model"},
		{"anthropic without key", ModelConfig{Provider: ProviderAnthropic, Model: "claude-test"}, "needs an API key"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewProvider(testCase.cfg); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error = %v, want one containing %q", err, testCase.want)
			}
		})
	}
}

func TestNewProviderDefaultsTheEndpoint(t *testing.T) {
	openai, err := NewProvider(ModelConfig{Provider: ProviderOpenAI, Model: "llama3.1"})
	if err != nil {
		t.Fatalf("openai: %v", err)
	}
	if got := openai.(*OpenAIProvider).cfg.BaseURL; got != defaultOpenAIBase {
		t.Errorf("openai endpoint = %q, want %q", got, defaultOpenAIBase)
	}
	if got := openai.(*OpenAIProvider).cfg.Workspace; got != "." {
		t.Errorf("workspace = %q, want the current directory", got)
	}

	anthropicProvider, err := NewProvider(ModelConfig{Provider: ProviderAnthropic, Model: "claude-test", APIKey: "k"})
	if err != nil {
		t.Fatalf("anthropic: %v", err)
	}
	if got := anthropicProvider.(*AnthropicProvider).cfg.BaseURL; got != defaultAnthropicBase {
		t.Errorf("anthropic endpoint = %q, want %q", got, defaultAnthropicBase)
	}

	echo, err := NewProvider(ModelConfig{})
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	if echo.Name() != ProviderEcho {
		t.Errorf("name = %q, want echo", echo.Name())
	}
}
