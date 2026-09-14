package gateway

import (
	"encoding/json"
	"strings"
	"unicode"
)

// needsRewrite reports whether the body carries a user-text container the
// gateway is responsible for. Bodies without input/messages are forwarded
// untouched unless they contain Han, which ServeHTTP rejects.
func needsRewrite(body []byte) bool {
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	_, hasInput := payload["input"]
	_, hasMessages := payload["messages"]
	return hasInput || hasMessages
}

// walkTranslatableTexts rewrites every translatable position the gateway recognises:
// the Responses string form of input, raw string items inside input/messages
// arrays, and content of the roles in isTranslatedRole. Anything else rides
// along untouched: an untranslatable turn is still a forwardable turn.
func walkTranslatableTexts(payload map[string]any, fn func(string) (string, error)) error {
	for _, key := range []string{"input", "messages"} {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		if text, ok := raw.(string); ok {
			out, err := fn(text)
			if err != nil {
				return err
			}
			payload[key] = out
			continue
		}
		if err := walkItems(raw, fn); err != nil {
			return err
		}
	}
	return nil
}

func walkItems(raw any, fn func(string) (string, error)) error {
	items, ok := raw.([]any)
	if !ok {
		// Not an item list (and not the string form, which the caller
		// handles): nothing addressable to translate, leave it for the
		// forward-as-is path.
		return nil
	}
	for i, item := range items {
		if text, ok := item.(string); ok {
			out, err := fn(text)
			if err != nil {
				return err
			}
			items[i] = out
			continue
		}
		m, ok := item.(map[string]any)
		if !ok {
			// Scalar item: hold out as-is, keep walking siblings.
			continue
		}
		role, _ := m["role"].(string)
		if role == "" {
			// Not a message item (function_call_output, reasoning, ...).
			// Tool I/O rides along untranslated per policy; hold the
			// item out and keep walking siblings.
			continue
		}
		if !isTranslatedRole(role) {
			continue
		}
		if err := rewriteContent(m, "content", fn); err != nil {
			return err
		}
	}
	return nil
}

// isTranslatedRole names the message roles whose content enters the
// translation path instead of failing closed on Han.
//
// user: the prompt itself. assistant: the model's own earlier replies,
// which the Reply Instruction makes Chinese and Codex replays as history
// on every later turn. Left on 403 they kill every multi-turn session at
// turn two. Translating them is bounded (a reply is translated once, the
// turn it first appears, then hits the cache) and coherent (the model
// sees an all-English transcript plus a standing reply-in-Chinese rule).
//
// system / developer / tool items ride along untranslated per policy:
// instructions are a one-time fix for the user to make English, and tool
// I/O is real file content, where a translation would desync history
// from disk.
func isTranslatedRole(role string) bool {
	return strings.EqualFold(role, "user") || strings.EqualFold(role, "assistant")
}

// isTranslatablePart names the content part types whose text enters
// translation (or carries the Reply Instruction): user prose (input_text /
// text) plus assistant history (output_text). Single source of truth for
// both the translation walk and the instruction prepend.
func isTranslatablePart(typ string) bool {
	switch typ {
	case "input_text", "text", "output_text":
		return true
	default:
		return false
	}
}

func rewriteContent(parent map[string]any, key string, fn func(string) (string, error)) error {
	switch content := parent[key].(type) {
	case string:
		out, err := fn(content)
		if err != nil {
			return err
		}
		parent[key] = out
	case []any:
		for _, part := range content {
			pm, ok := part.(map[string]any)
			if !ok {
				// Scalar part: hold out as-is, keep walking siblings.
				continue
			}
			typ, _ := pm["type"].(string)
			// output_text is how an assistant reply comes back as history.
			if isTranslatablePart(typ) {
				text, ok := pm["text"].(string)
				if !ok {
					continue
				}
				out, err := fn(text)
				if err != nil {
					return err
				}
				pm["text"] = out
				continue
			}
			// Non-text part (input_image, ...) and unknown types ride
			// along untouched; an untranslatable turn is still forwardable.

		}
	}
	return nil
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
