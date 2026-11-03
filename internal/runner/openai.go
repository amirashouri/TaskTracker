package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// OpenAIProvider speaks the /chat/completions dialect shared by OpenAI, Ollama,
// LM Studio, llama.cpp's server and vLLM. One shape covers a local model and a
// hosted one, which is why there is no separate integration per vendor.
type OpenAIProvider struct{ cfg ModelConfig }

func (p *OpenAIProvider) Name() string { return ProviderOpenAI }

func (p *OpenAIProvider) Execute(ctx context.Context, run Run, emit func(Event)) Result {
	ws, err := newWorkspace(p.cfg.Workspace)
	if err != nil {
		return Result{Outcome: "failed", Summary: "the workspace is unusable", Detail: err.Error()}
	}
	return runToolLoop(ctx, &openAIChat{cfg: p.cfg}, run, ws, p.cfg.MaxSteps, emit)
}

type openAIChat struct{ cfg ModelConfig }

func (c *openAIChat) Label() string { return ProviderOpenAI + " " + c.cfg.Model }

type openAIRequest struct {
	Model      string          `json:"model"`
	Messages   []openAIMessage `json:"messages"`
	Tools      []openAITool    `json:"tools,omitempty"`
	ToolChoice string          `json:"tool_choice,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

func (c *openAIChat) Reply(ctx context.Context, system string, messages []Message, tools []ToolSpec) (Message, error) {
	request := openAIRequest{Model: c.cfg.Model}

	// The system prompt is a message in this dialect, unlike Anthropic's top-level
	// field; that difference is the whole reason the adapters exist.
	request.Messages = append(request.Messages, openAIMessage{Role: "system", Content: system})
	for _, message := range messages {
		request.Messages = append(request.Messages, toOpenAIMessage(message))
	}
	for _, tool := range tools {
		request.Tools = append(request.Tools, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		})
	}
	if len(request.Tools) > 0 {
		request.ToolChoice = "auto"
	}

	headers := map[string]string{}
	if c.cfg.APIKey != "" {
		headers["Authorization"] = "Bearer " + c.cfg.APIKey
	}

	var response struct {
		Choices []struct {
			Message struct {
				Content   string           `json:"content"`
				ToolCalls []openAIToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := postModel(ctx, c.cfg.BaseURL+"/chat/completions", headers, request, &response); err != nil {
		return Message{}, err
	}
	if len(response.Choices) == 0 {
		return Message{}, errors.New("the model returned no choice")
	}

	choice := response.Choices[0].Message
	reply := Message{Role: "assistant", Content: strings.TrimSpace(choice.Content)}
	for index, call := range choice.ToolCalls {
		arguments := strings.TrimSpace(call.Function.Arguments)
		if arguments == "" {
			arguments = "{}"
		}
		// Older local servers omit the id. A stable local one is required, because
		// the tool result has to name the call it answers.
		id := call.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", index)
		}
		reply.ToolCalls = append(reply.ToolCalls, ToolCall{
			ID:        id,
			Name:      call.Function.Name,
			Arguments: arguments,
		})
	}
	return reply, nil
}

func toOpenAIMessage(message Message) openAIMessage {
	if message.Role == "tool" {
		return openAIMessage{
			Role:       "tool",
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
		}
	}

	out := openAIMessage{Role: message.Role, Content: message.Content}
	for _, call := range message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, openAIToolCall{
			ID:   call.ID,
			Type: "function",
			Function: openAIFunctionCall{
				Name:      call.Name,
				Arguments: call.Arguments,
			},
		})
	}
	return out
}
