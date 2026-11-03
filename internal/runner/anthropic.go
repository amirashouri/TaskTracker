package runner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// AnthropicProvider speaks the Messages API. It differs from the OpenAI dialect in
// three ways that matter here: the system prompt is a top-level field, tool calls
// are content blocks, and tool results are user-role blocks grouped per turn.
type AnthropicProvider struct{ cfg ModelConfig }

func (p *AnthropicProvider) Name() string { return ProviderAnthropic }

func (p *AnthropicProvider) Execute(ctx context.Context, run Run, emit func(Event)) Result {
	ws, err := newWorkspace(p.cfg.Workspace)
	if err != nil {
		return Result{Outcome: "failed", Summary: "the workspace is unusable", Detail: err.Error()}
	}
	return runToolLoop(ctx, &anthropicChat{cfg: p.cfg}, run, ws, p.cfg.MaxSteps, emit)
}

type anthropicChat struct{ cfg ModelConfig }

func (c *anthropicChat) Label() string { return ProviderAnthropic + " " + c.cfg.Model }

const (
	anthropicVersion   = "2023-06-01"
	anthropicMaxTokens = 8192
)

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

func (c *anthropicChat) Reply(ctx context.Context, system string, messages []Message, tools []ToolSpec) (Message, error) {
	request := anthropicRequest{
		Model:     c.cfg.Model,
		MaxTokens: anthropicMaxTokens,
		System:    system,
		Messages:  toAnthropicMessages(messages),
	}
	for _, tool := range tools {
		request.Tools = append(request.Tools, anthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.Parameters,
		})
	}

	headers := map[string]string{
		"x-api-key":         c.cfg.APIKey,
		"anthropic-version": anthropicVersion,
	}

	var response struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := postModel(ctx, c.cfg.BaseURL+"/v1/messages", headers, request, &response); err != nil {
		return Message{}, err
	}

	reply := Message{Role: "assistant"}
	var texts []string
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				texts = append(texts, block.Text)
			}
		case "tool_use":
			arguments := strings.TrimSpace(string(block.Input))
			if arguments == "" || arguments == "null" {
				arguments = "{}"
			}
			reply.ToolCalls = append(reply.ToolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: arguments,
			})
		}
	}
	reply.Content = strings.TrimSpace(strings.Join(texts, "\n\n"))
	if reply.Content == "" && len(reply.ToolCalls) == 0 {
		return Message{}, errors.New("the model returned no text and no tool call")
	}
	return reply, nil
}

// toAnthropicMessages groups consecutive tool results into one user turn. The API
// requires alternating roles, and one tool_result block per tool call; emitting
// one message per result is rejected with a 400.
func toAnthropicMessages(messages []Message) []anthropicMessage {
	var out []anthropicMessage
	for index := 0; index < len(messages); {
		message := messages[index]

		switch message.Role {
		case "assistant":
			var blocks []anthropicBlock
			if text := strings.TrimSpace(message.Content); text != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: text})
			}
			for _, call := range message.ToolCalls {
				input := json.RawMessage(call.Arguments)
				if !json.Valid(input) {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    call.ID,
					Name:  call.Name,
					Input: input,
				})
			}
			if len(blocks) > 0 {
				out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
			}
			index++

		case "tool":
			var blocks []anthropicBlock
			for index < len(messages) && messages[index].Role == "tool" {
				result := messages[index]
				blocks = append(blocks, anthropicBlock{
					Type:      "tool_result",
					ToolUseID: result.ToolCallID,
					Content:   result.Content,
					IsError:   result.IsError,
				})
				index++
			}
			out = append(out, anthropicMessage{Role: "user", Content: blocks})

		default:
			out = append(out, anthropicMessage{
				Role:    "user",
				Content: []anthropicBlock{{Type: "text", Text: message.Content}},
			})
			index++
		}
	}
	return out
}
