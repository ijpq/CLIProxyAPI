package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToOpenAIResponses_CreatedIncludesOriginalRequestModel(t *testing.T) {
	request := []byte(`{"model":"original-codex-model"}`)
	translatedRequest := []byte(`{"model":"translated-codex-model"}`)
	for eventName, raw := range map[string][]byte{
		"response.created":     []byte(`data: {"type":"response.created","response":{"id":"resp_1"}}`),
		"response.in_progress": []byte(`data: {"type":"response.in_progress","response":{"id":"resp_1"}}`),
	} {
		outputs := ConvertCodexResponseToOpenAIResponses(context.Background(), "fallback-model", request, translatedRequest, raw, nil)
		if len(outputs) != 1 {
			t.Fatalf("%s outputs = %d, want 1", eventName, len(outputs))
		}
		if got := gjson.GetBytes(outputs[0], "response.model").String(); got != "original-codex-model" {
			t.Fatalf("%s models = %q, want original-codex-model; payload=%s", eventName, got, outputs[0])
		}
	}
}

// TestCompactionItemPreservedNonStream verifies the Codex->Responses non-stream
// translator keeps a type="compaction" output item (with its opaque
// encrypted_content) in response.output. Remote Compaction V2 requires exactly
// one such item to survive back to Codex.
func TestCompactionItemPreservedNonStream(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_c","status":"completed","output":[{"type":"compaction","id":"cmp_1","encrypted_content":"OPAQUE_ENC=="}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)

	out := ConvertCodexResponseToOpenAIResponsesNonStream(context.Background(), "gpt-5.6-sol", nil, nil, raw, nil)

	output := gjson.GetBytes(out, "output")
	if !output.IsArray() || len(output.Array()) != 1 {
		t.Fatalf("expected exactly one output item; payload=%s", out)
	}
	item := output.Array()[0]
	if item.Get("type").String() != "compaction" {
		t.Fatalf("output[0].type = %q, want compaction; payload=%s", item.Get("type").String(), out)
	}
	if item.Get("encrypted_content").String() != "OPAQUE_ENC==" {
		t.Fatalf("compaction encrypted_content lost; payload=%s", out)
	}
}

// TestCompactionSSEEventPassthrough verifies the streaming translator forwards a
// compaction output_item SSE event unchanged (opaque state intact), so Codex can
// collect it from the stream.
func TestCompactionSSEEventPassthrough(t *testing.T) {
	line := []byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"compaction","id":"cmp_1","encrypted_content":"OPAQUE_ENC=="}}`)

	out := ConvertCodexResponseToOpenAIResponses(context.Background(), "gpt-5.6-sol", nil, nil, line, nil)
	if len(out) != 1 {
		t.Fatalf("expected one forwarded frame, got %d", len(out))
	}
	frame := out[0]
	if item := gjson.GetBytes(frame[len("data: "):], "item"); item.Get("type").String() != "compaction" {
		t.Fatalf("compaction SSE event not passed through: %s", frame)
	}
	if item := gjson.GetBytes(frame[len("data: "):], "item"); item.Get("encrypted_content").String() != "OPAQUE_ENC==" {
		t.Fatalf("compaction SSE encrypted_content lost: %s", frame)
	}
}

func TestConvertCodexResponseToOpenAIResponsesNonStreamIncomplete(t *testing.T) {
	raw := []byte(`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)

	out := ConvertCodexResponseToOpenAIResponsesNonStream(context.Background(), "gpt-5.5", nil, nil, raw, nil)

	if got := gjson.GetBytes(out, "status").String(); got != "incomplete" {
		t.Fatalf("status = %q, want incomplete; payload=%s", got, out)
	}
	if got := gjson.GetBytes(out, "incomplete_details.reason").String(); got != "max_output_tokens" {
		t.Fatalf("incomplete reason = %q, want max_output_tokens; payload=%s", got, out)
	}
}
