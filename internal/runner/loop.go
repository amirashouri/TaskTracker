package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Message is one turn of a model conversation. Each adapter translates it to its
// own wire shape; the loop knows only these fields, which is what keeps the tool
// loop provider-independent.
type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	ToolName   string
	IsError    bool
}

// ToolCall is one invocation the model asked for. Arguments stays a raw JSON
// object so an adapter never has to guess at a schema it did not define.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ToolSpec is a tool declaration; Parameters is a JSON Schema object.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// chatModel is one provider's conversation endpoint. Separating it from Provider
// lets the loop be tested without HTTP and each adapter without the loop.
type chatModel interface {
	Reply(ctx context.Context, system string, messages []Message, tools []ToolSpec) (Message, error)
	Label() string
}

const defaultMaxSteps = 25

// systemPrompt frames the job. The task snapshot arrives from the app and is
// labelled as data in the user message rather than folded in here.
const systemPrompt = `You are a coding agent working on the developer's own machine, implementing a task from TaskTracker.

Rules:
- Work only inside the workspace directory named in the user message.
- Inspect before you edit. Prefer small, reviewable changes.
- Use the tools for every file access; never claim a change you did not make.
- Run the project's own checks when the workspace has them.
- When the task is done, reply with a short summary and make no further tool call.
- If you cannot finish, say what blocked you and stop.`

// runToolLoop is the whole agent: ask, run any tools, feed the results back, and
// repeat until the model answers without a tool call or the step budget runs out.
func runToolLoop(ctx context.Context, chat chatModel, run Run, ws workspace, maxSteps int, emit func(Event)) Result {
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}

	messages := []Message{{
		Role: "user",
		Content: fmt.Sprintf(
			"Workspace: %s\n\nTask snapshot (JSON):\n%s\n\nImplement this task.",
			ws.root, strings.TrimSpace(run.TaskDocument),
		),
	}}
	tools := runnerTools()

	emit(Event{Kind: "status", Message: "agent: " + chat.Label() + " is reading the task"})

	for step := 1; step <= maxSteps; step++ {
		if ctx.Err() != nil {
			return Result{Outcome: "cancelled", Summary: "the run was cancelled"}
		}

		reply, err := chat.Reply(ctx, systemPrompt, messages, tools)
		if err != nil {
			if ctx.Err() != nil {
				return Result{Outcome: "cancelled", Summary: "the run was cancelled"}
			}
			emit(Event{Kind: "log", Message: "agent: " + truncate(err.Error(), 400)})
			return Result{Outcome: "failed", Summary: "the model call failed", Detail: err.Error()}
		}
		messages = append(messages, reply)

		if len(reply.ToolCalls) == 0 {
			summary := strings.TrimSpace(reply.Content)
			if summary == "" {
				return Result{Outcome: "failed", Summary: "the model finished without a summary", Detail: "empty reply"}
			}
			summary = truncate(summary, 500)
			emit(Event{
				Kind:    "result",
				Message: summary,
				Payload: fmt.Sprintf(`{"steps":%d}`, step),
			})
			return Result{Outcome: "succeeded", Summary: summary}
		}

		emit(Event{
			Kind:    "status",
			Message: fmt.Sprintf("agent: step %d of %d", step, maxSteps),
			Payload: fmt.Sprintf(`{"steps":%d}`, step),
		})

		for _, call := range reply.ToolCalls {
			if ctx.Err() != nil {
				return Result{Outcome: "cancelled", Summary: "the run was cancelled"}
			}

			emit(Event{Kind: "tool", Message: call.Name, Payload: toolPayload(call)})
			output, failed := executeTool(ctx, ws, call)
			emit(Event{
				Kind:    "log",
				Message: truncate(firstLine(call.Name+": "+output), 300),
				Payload: fmt.Sprintf(`{"tool":%q,"failed":%t}`, call.Name, failed),
			})

			messages = append(messages, Message{
				Role:       "tool",
				Content:    output,
				ToolCallID: call.ID,
				ToolName:   call.Name,
				IsError:    failed,
			})
		}
	}

	return Result{
		Outcome: "failed",
		Summary: fmt.Sprintf("the agent stopped after %d steps without finishing", maxSteps),
		Detail:  "step limit reached",
	}
}

// toolPayload gives the app something to render without parsing the message.
func toolPayload(call ToolCall) string {
	payload, err := json.Marshal(map[string]string{
		"tool":      call.Name,
		"arguments": truncate(call.Arguments, 400),
	})
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}
