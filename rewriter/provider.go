package rewriter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	providerAnthropic = "anthropic"
	providerOpenAI    = "openai"

	anthropicEndpoint = "https://api.anthropic.com/v1/messages"
	anthropicVersion  = "2023-06-01"
	openAIEndpoint    = "https://api.openai.com/v1/chat/completions"

	// Errors are read back for diagnosis, not stored, so the cap only has to be
	// large enough to carry a provider error envelope.
	maxErrorBody            = 4 << 10
	maxProviderResponseBody = 4 << 20
)

// completion is one rewriter call's result. Token counts are the provider's own
// usage figures; they are left at zero when the provider reports none, never
// estimated, because they are what a claimed saving is netted against.
//
// stopReason is carried because a completion cut off at the output ceiling
// looks exactly like a short one. The gate cannot tell them apart — a truncated
// rewrite can preserve every failure the model had emitted so far and silently
// drop the rest — so truncation is rejected on the provider's own signal.
type completion struct {
	text         string
	stopReason   string
	inputTokens  int
	outputTokens int
}

// truncatedStopReasons are the two providers' spellings for "hit the output
// cap". Anything else, including an unrecognised value, is treated as a normal
// stop: this list only ever adds rejections.
var truncatedStopReasons = map[string]bool{
	"max_tokens": true, // anthropic
	"length":     true, // openai
}

func (c *Client) complete(ctx context.Context, user string, maxOutput int) (completion, error) {
	switch c.provider {
	case providerAnthropic:
		return c.completeAnthropic(ctx, user, maxOutput)
	case providerOpenAI:
		return c.completeOpenAI(ctx, user, maxOutput)
	default:
		// Unreachable: New rejects unknown providers. Fail closed anyway.
		return completion{}, fmt.Errorf("rewriter: unsupported provider %q", c.provider)
	}
}

func (c *Client) completeAnthropic(ctx context.Context, user string, maxOutput int) (completion, error) {
	body := map[string]any{
		"model":       c.model,
		"max_tokens":  maxOutput,
		"temperature": 0,
		"system":      systemPrompt,
		"messages": []map[string]any{
			{"role": "user", "content": user},
		},
	}
	raw, err := c.do(ctx, anthropicEndpoint, body, map[string]string{
		"x-api-key":         c.apiKey,
		"anthropic-version": anthropicVersion,
	})
	if err != nil {
		return completion{}, err
	}

	var decoded struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return completion{}, fmt.Errorf("rewriter: decode anthropic response: %w", err)
	}
	var text strings.Builder
	for _, block := range decoded.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return completion{
		text:         text.String(),
		stopReason:   decoded.StopReason,
		inputTokens:  decoded.Usage.Input,
		outputTokens: decoded.Usage.Output,
	}, nil
}

func (c *Client) completeOpenAI(ctx context.Context, user string, maxOutput int) (completion, error) {
	body := map[string]any{
		"model":                 c.model,
		"max_completion_tokens": maxOutput,
		"messages": []map[string]any{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": user},
		},
	}
	// The reasoning-tier models reject any temperature other than the default,
	// and gpt-5-mini — the paper's own rewriter and this module's documented
	// OpenAI model — is one of them. Sending temperature 0 there turns every
	// rewrite into a 400, so the field is omitted for those families and the
	// determinism requirement is carried by the prompt instead.
	if openAIAcceptsTemperature(c.model) {
		body["temperature"] = 0
	}
	raw, err := c.do(ctx, openAIEndpoint, body, map[string]string{
		"Authorization": "Bearer " + c.apiKey,
	})
	if err != nil {
		return completion{}, err
	}

	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return completion{}, fmt.Errorf("rewriter: decode openai response: %w", err)
	}
	var text, stop string
	if len(decoded.Choices) > 0 {
		text = decoded.Choices[0].Message.Content
		stop = decoded.Choices[0].FinishReason
	}
	return completion{
		text:         text,
		stopReason:   stop,
		inputTokens:  decoded.Usage.Prompt,
		outputTokens: decoded.Usage.Completion,
	}, nil
}

// openAIAcceptsTemperature reports whether a chat-completions model still takes
// a caller-set temperature. The reasoning families (gpt-5*, o1/o3/o4*) do not.
func openAIAcceptsTemperature(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{"gpt-5", "o1", "o3", "o4"} {
		if name == prefix || strings.HasPrefix(name, prefix+"-") || strings.HasPrefix(name, prefix+".") {
			return false
		}
	}
	return true
}

func (c *Client) do(ctx context.Context, endpoint string, body map[string]any, headers map[string]string) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("rewriter: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("rewriter: build request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := c.doer(req)
	if err != nil {
		return nil, fmt.Errorf("rewriter: %s: %w", endpoint, err)
	}
	// An injected doer is caller code; a nil response or body must surface as an
	// error rather than a panic inside the proxy hot path.
	if resp == nil {
		return nil, fmt.Errorf("rewriter: %s: nil response", endpoint)
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, fmt.Errorf("rewriter: %s: status %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("rewriter: read response: %w", err)
	}
	if len(raw) > maxProviderResponseBody {
		return nil, fmt.Errorf("rewriter: response exceeds %d bytes", maxProviderResponseBody)
	}
	return raw, nil
}
