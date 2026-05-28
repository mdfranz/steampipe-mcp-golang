package main

import (
	"testing"
)

func TestEnforcePayloadLimit_NoTruncation(t *testing.T) {
	res := &QueryResult{
		Rows: []map[string]any{
			{"col1": "val1"},
			{"col2": "val2"},
		},
		RowCount: 2,
	}

	// Large payload limit, should remain completely untouched
	out, err := EnforcePayloadLimit(res, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Truncated {
		t.Error("expected no truncation")
	}
	if len(out.Rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(out.Rows))
	}
}

func TestEnforcePayloadLimit_WithTruncation(t *testing.T) {
	res := &QueryResult{
		Rows: []map[string]any{
			{"col": "verylongvalueheretoexceedthelimitsofthetest"},
			{"col": "anotherlongvalueheretoexceedthelimitsofthetest"},
			{"col": "yetanotherlongvalueheretoexceedthelimitsofthetest"},
		},
		RowCount: 3,
	}

	// Set a very small maxBytes so that only 1 or 2 rows fit
	out, err := EnforcePayloadLimit(res, 160)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !out.Truncated {
		t.Error("expected truncation")
	}
	if out.TruncationReason != "payload_cap" {
		t.Errorf("expected payload_cap reason, got %q", out.TruncationReason)
	}
	if len(out.Rows) >= 3 {
		t.Errorf("expected trimmed rows, got %d", len(out.Rows))
	}
}
