// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import "encoding/json"

type CacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type TextBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type ImageBlock struct {
	Type         string        `json:"type"`
	Source       ImageSource   `json:"source"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

type ToolUseBlock struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input any    `json:"input"`
}

type ToolResultBlock struct {
	Type         string        `json:"type"`
	ToolUseID    string        `json:"tool_use_id"`
	Content      any           `json:"content"`
	IsError      bool          `json:"is_error,omitempty"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

type ContentBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	Source       *ImageSource    `json:"source,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        any             `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      any             `json:"content,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	CacheControl *CacheControl   `json:"cache_control,omitempty"`
	Raw          json.RawMessage `json:"-"`
}

type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type ToolDef struct {
	Name         string        `json:"name"`
	Description  string        `json:"description,omitempty"`
	InputSchema  any           `json:"input_schema,omitempty"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

type SystemField = any

type Usage struct {
	InputTokens              int                 `json:"input_tokens,omitempty"`
	OutputTokens             int                 `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int                 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int                 `json:"cache_read_input_tokens,omitempty"`
	CacheCreation            *CacheCreationUsage `json:"cache_creation,omitempty"`
	ServerToolUse            *ServerToolUse      `json:"server_tool_use,omitempty"`
	CachedTokens             int                 `json:"cached_tokens,omitempty"`
}

type CacheCreationUsage struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens,omitempty"`
}

type ServerToolUse struct {
	WebSearchRequests int `json:"web_search_requests,omitempty"`
}

type MessagesRequest struct {
	Model    string      `json:"model"`
	Messages []Message   `json:"messages"`
	System   SystemField `json:"system,omitempty"`
	Tools    []ToolDef   `json:"tools,omitempty"`
}

type TransformOptions struct {
	Model                      string
	geminiMediaResolution      string
	Compress                   bool
	CompressTools              bool
	CompressReminders          bool
	CompressToolResults        bool
	MinCompressChars           int
	MinReminderChars           int
	MinToolResultChars         int
	Cols                       int
	MaxImagesPerToolResult     int
	MultiCol                   int
	CharsPerToken              float64
	HistoryAmortizationHorizon int
	PriorWarmTokens            float64
	PriorWarmImageTokens       float64
	CollapseHistory            bool
	Reflow                     bool
	KeepSharp                  func(KeepSharpBlock) bool
	EmitRecoverable            bool
}

type KeepSharpBlock struct{ Kind, Text, ToolUseID string }

type RecoverableBlock struct {
	ID, Kind, ToolUseID, Text string
	ImageCount                int
}

type TransformInfo struct {
	Compressed                          bool
	Reason                              string
	OrigChars, CompressedChars          int
	ImageCount, ImageBytes, ImagePixels int
	TextTokensEstimate                  int
	ImageTokensEstimate                 int
	StaticChars, DynamicChars           int
	DynamicBlockCount                   int
	UnknownStaticTags                   []string `json:"unknownStaticTags,omitempty"`
	ChurningStaticTags                  []string `json:"churningStaticTags,omitempty"`
	ReminderImgs, ToolResultImgs        int
	DroppedChars                        int
	PassthroughReasons                  map[string]int
	KeptSharpBlocks                     int
	TruncatedToolResults                int
	OmittedChars                        int
	CollapsedTurns, CollapsedChars      int
	CollapsedImages                     int
	HistoryReason                       string
	Recoverable                         []RecoverableBlock
}

func DefaultTransformOptions(model string) TransformOptions {
	return TransformOptions{
		Model:                      model,
		Compress:                   true,
		CompressTools:              true,
		CompressReminders:          true,
		CompressToolResults:        true,
		MinCompressChars:           2000,
		MinReminderChars:           6000,
		MinToolResultChars:         6000,
		Cols:                       DefaultCols,
		MaxImagesPerToolResult:     10,
		MultiCol:                   1,
		CharsPerToken:              4,
		HistoryAmortizationHorizon: 1,
		CollapseHistory:            true,
		Reflow:                     true,
		KeepSharp:                  func(KeepSharpBlock) bool { return false },
		EmitRecoverable:            false,
	}
}
