package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// chatCompletions sends an ordinary system message, which is supported by local servers as well as hosted APIs.
func (c *Client) chatCompletions(ctx context.Context, hc *http.Client, key string, r Request) (Reply, bool, error) {
	turns := make([]Message, 0, len(r.Messages)+1)
	if r.System != "" {
		turns = append(turns, Message{Role: "system", Content: r.System})
	}
	turns = append(turns, r.Messages...)
	// OpenAI-contract servers stream the final usage chunk only when asked for it.
	body := map[string]any{"model": c.cfg.Model, "messages": turns, "stream": true, "stream_options": map[string]any{"include_usage": true}}
	if r.MaxTokens > 0 {
		body["max_tokens"] = r.MaxTokens
	}
	effort := effortForRequest(c.cfg.ReasoningEffort, r)
	ApplyReasoningEffort(body, "chat", c.cfg.Model, effort)
	droppedEffort := OmitChatEffort(c.cfg.BaseURL, key, c.cfg.Model, c.cfg.ViaMachine, effort)
	if droppedEffort {
		delete(body, "reasoning_effort")
	}
	// Each rejected field is changed at most once, so independent compatibility retries stay bounded.
	renamedTokens, droppedStreamOptions := false, false
	for {
		encoded, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiEndpoint(c.cfg.BaseURL, "chat/completions"), bytes.NewReader(encoded))
		if err != nil {
			return Reply{}, false, err
		}
		c.setCustomHeaders(req)
		req.Header.Set("content-type", "application/json")
		req.Header.Set("accept", "text/event-stream")
		req.Header.Set("authorization", "Bearer "+key)
		c.setGatewayHeaders(ctx, req, r)
		resp, err := hc.Do(req)
		if err != nil {
			return Reply{}, false, fmt.Errorf("ai: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
			// Recognized machine codes always win over compatibility wording: a limit or a model/context/endpoint
			// failure must never trigger a modified request, even when its message names a rejected field.
			if HasRecognizedErrorCode(raw) {
				return Reply{}, false, c.newHTTPError(resp, raw, key)
			}
			// Some models reject max_tokens and explicitly ask for the newer name, and strict compatible servers
			// reject stream_options or reasoning_effort. Each such response leads to a modified request, not a replay
			// of the rejected one, so X-Should-Retry: false forbids neither the modified request nor learning from it.
			// Arbitrary 400s, non-off effort values, policy ceilings and started streams must never be replayed.
			fieldRejected := ChatRejectsReasoningEffort(raw)
			offValueRejected := !fieldRejected && ChatNoThinking(effort) && ChatRejectsNoEffortValue(raw)
			if !droppedEffort && effort != "" && resp.StatusCode == http.StatusBadRequest && (fieldRejected || offValueRejected) {
				RememberChatEffortRejection(c.cfg.BaseURL, key, c.cfg.Model, c.cfg.ViaMachine, offValueRejected)
				droppedEffort = true
				delete(body, "reasoning_effort")
				logf("ai: chat/completions: gateway rejected reasoning_effort; retrying once without it")
				continue
			}
			if !renamedTokens && r.MaxTokens > 0 && resp.StatusCode == http.StatusBadRequest && ChatRequiresCompletionTokens(raw) {
				renamedTokens = true
				delete(body, "max_tokens")
				body["max_completion_tokens"] = r.MaxTokens
				continue
			}
			if !droppedStreamOptions && (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity) && ChatRejectsStreamOptions(raw) {
				droppedStreamOptions = true
				delete(body, "stream_options")
				continue
			}
			return Reply{}, false, c.newHTTPError(resp, raw, key)
		}
		reply, err := readCompletions(resp.Body)
		resp.Body.Close()
		var limit *streamLimit
		return reply, !errors.As(err, &limit), err
	}
}

// ChatNoThinking reports the product-level efforts that mean "disable thinking when possible".
func ChatNoThinking(effort string) bool {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "off", "none", "disabled":
		return true
	}
	return false
}

const (
	chatEffortCompatTTL   = time.Hour
	chatEffortCompatLimit = 128
)

// chatEffortCompatibility separates a gateway that rejects the whole field from one that rejects only "none".
// The latter must not suppress an explicit non-off level on a later call.
type chatEffortCompatibility struct {
	allUntil  time.Time
	noneUntil time.Time
}

// chatEffortCache is one process-wide, hard-bounded map, not an allowance per connection.
type chatEffortCache struct {
	mu      sync.Mutex
	entries map[string]chatEffortCompatibility
}

var processChatEffortCache chatEffortCache

func chatEffortCacheKey(baseURL, key, model, viaMachine string) string {
	return gateKeyFor("chat", baseURL, key) + "\x00" + viaMachine + "\x00" + model
}

// omitChatEffort says whether this connection/model already rejected the requested form of effort recently.
func (c *chatEffortCache) omitChatEffort(key, effort string) bool {
	if effort == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return false
	}
	current := now()
	if !entry.allUntil.After(current) && !entry.noneUntil.After(current) {
		delete(c.entries, key)
		return false
	}
	return entry.allUntil.After(current) || ChatNoThinking(effort) && entry.noneUntil.After(current)
}

// rememberChatEffortRejection learns only from an explicit rejection. noneOnly keeps explicit non-off levels eligible.
func (c *chatEffortCache) rememberChatEffortRejection(key string, noneOnly bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]chatEffortCompatibility{}
	}
	current := now()
	for id, entry := range c.entries {
		if !entry.allUntil.After(current) && !entry.noneUntil.After(current) {
			delete(c.entries, id)
		}
	}
	if len(c.entries) >= chatEffortCompatLimit {
		oldestID, oldest := "", current
		for id, entry := range c.entries {
			latest := entry.allUntil
			if entry.noneUntil.After(latest) {
				latest = entry.noneUntil
			}
			if oldestID == "" || latest.Before(oldest) {
				oldestID, oldest = id, latest
			}
		}
		delete(c.entries, oldestID)
	}
	entry := c.entries[key]
	if noneOnly {
		entry.noneUntil = current.Add(chatEffortCompatTTL)
	} else {
		entry.allUntil = current.Add(chatEffortCompatTTL)
	}
	c.entries[key] = entry
}

// OmitChatEffort applies a connection's learned rejection to a probe as well as a real call.
func OmitChatEffort(baseURL, key, model, viaMachine, effort string) bool {
	return processChatEffortCache.omitChatEffort(chatEffortCacheKey(baseURL, key, model, viaMachine), effort)
}

// RememberChatEffortRejection records a probe's explicit rejection for later probes and real calls.
func RememberChatEffortRejection(baseURL, key, model, viaMachine string, noneOnly bool) {
	processChatEffortCache.rememberChatEffortRejection(chatEffortCacheKey(baseURL, key, model, viaMachine), noneOnly)
}

// ChatRequiresCompletionTokens recognizes a rejected token field that explicitly requests its replacement.
func ChatRequiresCompletionTokens(raw []byte) bool {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return false
	}
	message := strings.ToLower(envelope.Error.Message)
	return strings.Contains(message, "max_tokens") && strings.Contains(message, "max_completion_tokens") &&
		(strings.Contains(message, "unsupported") || strings.Contains(message, "not supported"))
}

// ChatRejectsReasoningEffort reports an error body that rejects the reasoning_effort parameter itself. An unsupported
// level, an invalid value or an operator ceiling is a different error and must keep its original response. Callers
// restrict this compatibility retry to the HTTP status they accept.
func ChatRejectsReasoningEffort(raw []byte) bool {
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Detail  []struct {
			Loc []any  `json:"loc"`
			Msg string `json:"msg"`
		} `json:"detail"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		topCode := decodeErrorCode(envelope.Code)
		if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
			var object struct {
				Message json.RawMessage `json:"message"`
				Param   json.RawMessage `json:"param"`
				Code    json.RawMessage `json:"code"`
			}
			if json.Unmarshal(envelope.Error, &object) == nil {
				code := decodeErrorCode(object.Code)
				if code == "" {
					code = topCode
				}
				if chatReasoningValueCode(code) {
					return false
				}
				return chatReasoningFieldError(decodeErrorString(object.Message), decodeErrorString(object.Param))
			}
			var message string
			return !chatReasoningValueCode(topCode) && json.Unmarshal(envelope.Error, &message) == nil && chatReasoningFieldError(message, "")
		}
		if chatReasoningValueCode(topCode) {
			return false
		}
		if envelope.Message != "" {
			return chatReasoningFieldError(envelope.Message, "")
		}
		for _, detail := range envelope.Detail {
			for _, part := range detail.Loc {
				if part == "reasoning_effort" && chatReasoningFieldError(detail.Msg, "reasoning_effort") {
					return true
				}
			}
		}
		return false
	}
	return chatReasoningFieldError(string(raw), "")
}

// chatReasoningValueCode vetoes a retry when a structured error code already identifies a value or policy rejection,
// even if the message is only a generic "unsupported parameter".
func chatReasoningValueCode(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	for _, marker := range []string{"ceiling", "unsupported_value", "invalid_value", "unsupported_reasoning_effort", "invalid_reasoning_effort"} {
		if strings.Contains(code, marker) {
			return true
		}
	}
	return false
}

func chatReasoningCeilingCode(code string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(code)), "ceiling")
}

// ChatRejectsNoEffortValue recognizes a value-level rejection of "none". Call it only for ChatNoThinking requests:
// explicit non-off levels deliberately keep their original errors.
func ChatRejectsNoEffortValue(raw []byte) bool {
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Detail  []struct {
			Loc []any  `json:"loc"`
			Msg string `json:"msg"`
		} `json:"detail"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		topCode := decodeErrorCode(envelope.Code)
		if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
			var object struct {
				Message json.RawMessage `json:"message"`
				Param   json.RawMessage `json:"param"`
				Code    json.RawMessage `json:"code"`
			}
			if json.Unmarshal(envelope.Error, &object) == nil {
				code, message, param := decodeErrorCode(object.Code), decodeErrorString(object.Message), decodeErrorString(object.Param)
				if code == "" {
					code = topCode
				}
				if chatReasoningCeilingCode(code) || strings.Contains(strings.ToLower(message), "reasoning_effort_above_ceiling") {
					return false
				}
				if chatReasoningValueCode(code) {
					return strings.EqualFold(strings.TrimSpace(param), "reasoning_effort") || chatNoEffortValueMessage(message, "")
				}
				return chatNoEffortValueMessage(message, param)
			}
			var message string
			return !chatReasoningCeilingCode(topCode) && json.Unmarshal(envelope.Error, &message) == nil && chatNoEffortValueMessage(message, "")
		}
		if chatReasoningCeilingCode(topCode) {
			return false
		}
		if envelope.Message != "" {
			return chatNoEffortValueMessage(envelope.Message, "")
		}
		for _, detail := range envelope.Detail {
			for _, part := range detail.Loc {
				if part == "reasoning_effort" && chatNoEffortValueMessage(detail.Msg, "reasoning_effort") {
					return true
				}
			}
		}
		return false
	}
	return chatNoEffortValueMessage(string(raw), "")
}

func chatNoEffortValueMessage(message, param string) bool {
	message = strings.ToLower(message)
	param = strings.ToLower(strings.TrimSpace(param))
	if strings.Contains(message, "reasoning_effort_above_ceiling") {
		return false
	}
	for _, valueError := range []string{"unsupported value", "invalid value", "supported values", "unsupported reasoning_effort", "invalid reasoning_effort",
		"input should be", "not one of", "must be one of", "not a valid enum", "not a valid enumeration"} {
		if strings.Contains(message, valueError) {
			return param == "reasoning_effort" || strings.Contains(message, "reasoning_effort") && strings.Contains(message, "none")
		}
	}
	return false
}

// chatReasoningFieldError matches only the error's own message and parameter, never an echoed request in its details.
func chatReasoningFieldError(message, param string) bool {
	message = strings.ToLower(message)
	param = strings.ToLower(strings.TrimSpace(param))
	if param != "" && param != "reasoning_effort" {
		return false
	}
	if !strings.Contains(message, "reasoning_effort") && param != "reasoning_effort" {
		return false
	}
	for _, valueError := range []string{
		"unsupported value", "invalid value", "supported values", "unsupported reasoning_effort", "invalid reasoning_effort",
		"reasoning_effort_above_ceiling", "reasoning_effort must be",
	} {
		if strings.Contains(message, valueError) {
			return false
		}
	}
	for _, fieldError := range []string{
		"unsupported parameter", "not supported parameter", "unknown parameter", "unrecognized parameter",
		"unrecognized request argument", "unknown field", "unrecognized field", "unexpected field",
		"extra inputs are not permitted", "additional properties are not allowed",
	} {
		if strings.Contains(message, fieldError) {
			return true
		}
	}
	// Gateways quote JSON strings inside their error message differently. Ignore quoting and whitespace only for the
	// field-only "reasoning_effort is not supported" form; a named value between those words does not match.
	compact := strings.NewReplacer(`\`, "", `"`, "", `'`, "", " ", "", "\t", "", "\r", "", "\n", "").Replace(message)
	return strings.Contains(compact, "reasoning_effortisnotsupported")
}

// ChatRejectsStreamOptions recognizes an error body that names the stream_options field. Such a server then
// streams no usage, and the reply reports zero.
func ChatRejectsStreamOptions(raw []byte) bool {
	body := strings.ToLower(string(raw))
	return strings.Contains(body, "stream_options") || strings.Contains(body, "include_usage")
}

// readCompletions consumes SSE events, including usage-only events after the finish reason.
func readCompletions(body io.Reader) (Reply, error) {
	var reply Reply
	var text strings.Builder
	var data []string
	finished, done := false, false
	refusal := "" // the RefusalError reason once the model declines: refusal, or content_filter
	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		if payload == "[DONE]" {
			done = true
			return nil
		}
		var chunk struct {
			Type    json.RawMessage `json:"type"`
			Model   string          `json:"model"`
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Content string `json:"content"`
					Refusal string `json:"refusal"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				PromptDetails    struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("ai: decode stream: %w", err)
		}
		rawError := bytes.TrimSpace(chunk.Error)
		hasError := len(rawError) > 0 && !bytes.Equal(rawError, []byte("null"))
		if hasError || decodeErrorString(chunk.Type) == "error" {
			code, msg := gatewayErrorDetails([]byte(payload))
			return streamFailureError("stream error", code, msg)
		}
		if chunk.Model != "" {
			reply.Model = chunk.Model
		}
		if u := chunk.Usage; u != nil {
			cached := max(0, min(u.PromptTokens, u.PromptDetails.CachedTokens))
			reply.Usage = Usage{InputTokens: u.PromptTokens - cached, OutputTokens: u.CompletionTokens, CacheReadInputTokens: cached}
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			text.WriteString(choice.Delta.Content)
			if choice.FinishReason == "content_filter" {
				refusal = "content_filter"
			} else if choice.Delta.Refusal != "" && refusal == "" {
				refusal = "refusal"
			}
			if choice.FinishReason != "" {
				finished = true
				reply.Truncated = choice.FinishReason == "length"
			}
		}
		return nil
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := consume(); err != nil {
				return Reply{}, err
			}
			if done {
				break
			}
		} else if value, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		// reply already carries any usage/model chunks read before the cut: keep them for the usage hook rather than
		// reporting the failed call as zero tokens. Its Text is still empty (assembled only after the scan).
		return reply, fmt.Errorf("ai: read stream: %w", err)
	}
	if err := consume(); err != nil {
		return Reply{}, err
	}
	if !done && !finished {
		return Reply{}, fmt.Errorf("ai: stream ended without a completed reply: %w", io.ErrUnexpectedEOF)
	}
	if refusal != "" {
		return reply, &RefusalError{StopReason: refusal}
	}
	reply.Text = text.String()
	if strings.TrimSpace(reply.Text) == "" && !reply.Truncated {
		return Reply{}, errors.New("ai: stream ended without a reply")
	}
	return reply, nil
}
