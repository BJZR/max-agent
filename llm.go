package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Msg struct {
	Role       string     `json:"role,omitempty"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type toolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function,omitempty"`
}

type chatReq struct {
	Model       string     `json:"model"`
	Messages    []Msg      `json:"messages"`
	Tools       []toolSpec `json:"tools,omitempty"`
	Stream      bool       `json:"stream,omitempty"`
	Temperature float64    `json:"temperature,omitempty"`
}

type chatDelta struct {
	Content   string     `json:"content,omitempty"`
	Reasoning string     `json:"reasoning_content,omitempty"`
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
}

type chatChunk struct {
	Choices []struct {
		Delta        chatDelta `json:"delta"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type Provider struct {
	config Config
	client *http.Client
}

func NewProvider(cfg Config) *Provider {
	return &Provider{
		config: cfg,
		client: &http.Client{Timeout: 15 * time.Minute},
	}
}

func (p *Provider) Chat(ctx context.Context, msgs []Msg, onToken, onReasoning func(string)) (Msg, error) {
	var tools []toolSpec
	if p.config.Tools {
		tools = toolSpecs
	}
	body, err := json.Marshal(chatReq{
		Model:       p.config.Model,
		Messages:    msgs,
		Tools:       tools,
		Stream:      true,
		Temperature: p.config.Temperature,
	})
	if err != nil {
		return Msg{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(p.config.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Msg{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return Msg{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Msg{}, fmt.Errorf("llm: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}

	assistant := Msg{Role: "assistant"}
	merged := []toolCall{}
	lastID := ""

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			return Msg{}, fmt.Errorf("llm: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		if d.Content != "" {
			assistant.Content += d.Content
			if onToken != nil {
				onToken(d.Content)
			}
		}
		if d.Reasoning != "" && onReasoning != nil {
			onReasoning(d.Reasoning)
		}
		for _, tc := range d.ToolCalls {
			if tc.ID != "" {
				merged = append(merged, tc)
				lastID = tc.ID
			} else if lastID != "" {
				for i := range merged {
					if merged[i].ID == lastID {
						merged[i].Function.Name += tc.Function.Name
						merged[i].Function.Arguments += tc.Function.Arguments
						break
					}
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return assistant, err
	}
	assistant.ToolCalls = merged
	return assistant, nil
}
