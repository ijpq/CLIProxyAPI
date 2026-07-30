package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

// TestCodexCompletedOutputRebuildPreservesCompaction verifies that when the
// Codex upstream omits response.output on the terminal event, the rebuild from
// collected response.output_item.done events keeps the type="compaction" item
// (with its opaque encrypted_content) exactly once, alongside other items.
//
// This exercises the streaming terminal path used by Remote Compaction V2.
func TestCodexCompletedOutputRebuildPreservesCompaction(t *testing.T) {
	byIndex := make(map[int64][]byte)
	var fallback [][]byte

	collectCodexOutputItemDone([]byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`), byIndex, &fallback)
	collectCodexOutputItemDone([]byte(`{"type":"response.output_item.done","output_index":1,"item":{"type":"compaction","id":"cmp_1","encrypted_content":"OPAQUE=="}}`), byIndex, &fallback)

	// Terminal event with an empty output array triggers the rebuild.
	completed := []byte(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`)
	patched := patchCodexCompletedOutput(completed, byIndex, fallback)

	output := gjson.GetBytes(patched, "response.output")
	if !output.IsArray() || len(output.Array()) != 2 {
		t.Fatalf("expected 2 rebuilt output items, got: %s", output.Raw)
	}
	compactionCount := 0
	for _, item := range output.Array() {
		if item.Get("type").String() == "compaction" {
			compactionCount++
			if item.Get("encrypted_content").String() != "OPAQUE==" {
				t.Fatalf("compaction encrypted_content lost: %s", item.Raw)
			}
		}
	}
	if compactionCount != 1 {
		t.Fatalf("expected exactly one compaction item, got %d; payload=%s", compactionCount, patched)
	}
}

// TestCodexCompletedOutputKeepsUpstreamCompaction verifies that when the upstream
// already includes response.output (with a compaction item), the terminal patch
// is a no-op and the compaction item is left untouched.
func TestCodexCompletedOutputKeepsUpstreamCompaction(t *testing.T) {
	byIndex := make(map[int64][]byte)
	var fallback [][]byte

	completed := []byte(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"compaction","id":"cmp_1","encrypted_content":"OPAQUE=="}]}}`)
	patched := patchCodexCompletedOutput(completed, byIndex, fallback)

	output := gjson.GetBytes(patched, "response.output")
	if !output.IsArray() || len(output.Array()) != 1 {
		t.Fatalf("expected upstream output preserved as-is, got: %s", output.Raw)
	}
	if output.Array()[0].Get("type").String() != "compaction" {
		t.Fatalf("compaction item altered: %s", patched)
	}
}
