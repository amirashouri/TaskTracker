package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scriptedChat answers with a fixed script, which is what makes the loop's
// control flow testable without a model or a server.
type scriptedChat struct {
	label string
	turns []Message
	err   error

	calls  int
	seen   []Message
	tools  int
	system string
}

func (s *scriptedChat) Reply(ctx context.Context, system string, messages []Message, tools []ToolSpec) (Message, error) {
	s.calls++
	s.system = system
	s.tools = len(tools)
	s.seen = append([]Message(nil), messages...)
	if s.err != nil {
		return Message{}, s.err
	}
	// A script that runs out keeps repeating its last turn, which is how a model
	// that never stops calling tools is modelled.
	index := s.calls - 1
	if index >= len(s.turns) {
		index = len(s.turns) - 1
	}
	return s.turns[index], nil
}

func (s *scriptedChat) Label() string { return s.label }

func TestToolLoopRunsToolsUntilTheModelStops(t *testing.T) {
	dir := t.TempDir()
	ws, err := newWorkspace(dir)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	arguments, err := json.Marshal(map[string]string{"path": "notes.txt", "content": "hello"})
	if err != nil {
		t.Fatalf("marshal arguments: %v", err)
	}

	chat := &scriptedChat{
		label: "scripted",
		turns: []Message{
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "write_file", Arguments: string(arguments)}}},
			{Role: "assistant", Content: "Wrote notes.txt."},
		},
	}

	var events []Event
	result := runToolLoop(context.Background(), chat,
		Run{ID: "run-1", TaskDocument: `{"item":{"key":"TT-1","title":"Write a note"}}`},
		ws, 5, func(event Event) { events = append(events, event) })

	if result.Outcome != "succeeded" {
		t.Fatalf("outcome = %q (%s), want succeeded", result.Outcome, result.Detail)
	}
	if result.Summary != "Wrote notes.txt." {
		t.Errorf("summary = %q, want the model's own words", result.Summary)
	}

	content, err := os.ReadFile(filepath.Join(dir, "notes.txt"))
	if err != nil {
		t.Fatalf("the tool did not write the file: %v", err)
	}
	if string(content) != "hello" {
		t.Errorf("file = %q, want hello", content)
	}

	kinds := map[string]int{}
	for _, event := range events {
		kinds[event.Kind]++
	}
	if kinds["tool"] != 1 || kinds["result"] != 1 {
		t.Errorf("events = %v, want one tool event and one result", kinds)
	}

	if chat.tools != len(runnerTools()) {
		t.Errorf("declared %d tools, want %d", chat.tools, len(runnerTools()))
	}
	if !strings.Contains(chat.system, "workspace") {
		t.Errorf("system prompt does not mention the workspace: %q", chat.system)
	}

	// The second call must carry the tool result, or the model cannot react to it.
	last := chat.seen[len(chat.seen)-1]
	if last.Role != "tool" || last.ToolCallID != "c1" {
		t.Errorf("last message = %+v, want the tool result for c1", last)
	}
	if !strings.Contains(last.Content, "notes.txt") {
		t.Errorf("tool result = %q, want it to name the file", last.Content)
	}
}

func TestToolLoopFailsAtTheStepLimit(t *testing.T) {
	ws, err := newWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	chat := &scriptedChat{
		label: "scripted",
		turns: []Message{{
			Role:      "assistant",
			ToolCalls: []ToolCall{{ID: "c1", Name: "list_files", Arguments: `{"path":"."}`}},
		}},
	}

	result := runToolLoop(context.Background(), chat, Run{}, ws, 3, func(Event) {})

	if result.Outcome != "failed" {
		t.Fatalf("outcome = %q, want failed at the step limit", result.Outcome)
	}
	if !strings.Contains(result.Detail, "step limit") {
		t.Errorf("detail = %q, want it to name the step limit", result.Detail)
	}
	if chat.calls != 3 {
		t.Errorf("model calls = %d, want exactly the budget of 3", chat.calls)
	}
}

func TestToolLoopReportsAModelFailure(t *testing.T) {
	ws, err := newWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	chat := &scriptedChat{label: "scripted", err: errors.New("connection refused")}
	result := runToolLoop(context.Background(), chat, Run{}, ws, 3, func(Event) {})

	if result.Outcome != "failed" || !strings.Contains(result.Detail, "connection refused") {
		t.Errorf("result = %+v, want a failure carrying the model error", result)
	}
}

func TestToolLoopStopsWhenTheRunIsCancelled(t *testing.T) {
	ws, err := newWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	chat := &scriptedChat{label: "scripted"}
	result := runToolLoop(ctx, chat, Run{}, ws, 3, func(Event) {})

	if result.Outcome != "cancelled" {
		t.Errorf("outcome = %q, want cancelled", result.Outcome)
	}
	if chat.calls != 0 {
		t.Errorf("model calls = %d, want none after cancellation", chat.calls)
	}
}
