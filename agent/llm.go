package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"alphathesis/client"
)

// ChatCompleter is the LLM capability required by every agent.
// *client.VLLMClient satisfies this interface.
type ChatCompleter interface {
	CreateChatCompletion(context.Context, client.ChatCompletionRequest) (*client.ChatCompletionResponse, error)
}

// FirstChoiceMessage returns the first choice message from a chat completion
// response, or an error when the response is empty.
func FirstChoiceMessage(resp *client.ChatCompletionResponse) (client.ChatMessage, error) {
	if resp == nil || len(resp.Choices) == 0 {
		return client.ChatMessage{}, errors.New("llm returned no choices")
	}
	return resp.Choices[0].Message, nil
}

// ChatContentString coerces a chat message Content field to a non-empty string.
// Content may be a plain string or a JSON-encoded value depending on the model.
func ChatContentString(content interface{}) (string, error) {
	switch v := content.(type) {
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return "", errors.New("llm returned empty content")
		}
		return v, nil
	case nil:
		return "", errors.New("llm returned nil content")
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("marshal non-string llm content: %w", err)
		}
		return string(data), nil
	}
}

// ExtractJSONObject strips markdown code fences if present and returns the
// substring from the first '{' to the last '}'. Useful when a model wraps its
// JSON output in ```json ... ```.
func ExtractJSONObject(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		return content[start : end+1]
	}
	return content
}

// ClampFloat clamps v to [min, max].
func ClampFloat(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
