package main

import (
	"errors"
	"strings"
	"testing"
)

func TestFormatDBError(t *testing.T) {
	tests := []struct {
		err      error
		expected string
	}{
		{
			err:      errors.New("canceling statement due to statement timeout"),
			expected: "Query exceeded statement_timeout. Try a tighter WHERE, LIMIT, or fewer columns; or raise STEAMPIPE_MCP_STATEMENT_TIMEOUT_MS.",
		},
		{
			err:      errors.New("dial tcp 127.0.0.1:9193: connect: connection refused"),
			expected: "Cannot reach Steampipe. Verify the service is running and STEAMPIPE_MCP_WORKSPACE_DATABASE points at it.",
		},
		{
			err:      errors.New("syntax error at or near \"FROM\""),
			expected: "SQL syntax error: syntax error at or near \"FROM\"",
		},
		{
			err:      errors.New("relation \"aws_s3_buckett\" does not exist"),
			expected: "Relation not found: relation \"aws_s3_buckett\" does not exist",
		},
		{
			err:      errors.New("some other error"),
			expected: "Database query execution error: some other error",
		},
	}

	for _, tc := range tests {
		got := formatDBError(tc.err)
		if got != tc.expected {
			t.Errorf("formatDBError(%q) = %q, expected %q", tc.err.Error(), got, tc.expected)
		}
	}
}

func TestFormatPluginListMarkdown(t *testing.T) {
	rows := []map[string]any{
		{
			"name":           "aws_connection",
			"plugin":         "aws",
			"plugin_version": "0.121.0",
		},
	}

	markdown := formatPluginListMarkdown(rows)
	if !strings.Contains(markdown, "aws_connection") || !strings.Contains(markdown, "0.121.0") {
		t.Errorf("unexpected plugin markdown: %q", markdown)
	}
}

func TestFormatTableListMarkdown(t *testing.T) {
	rows := []map[string]any{
		{
			"name":            "aws_s3_bucket",
			"connection_name": "aws_connection",
			"description":     "Represents S3 buckets.",
		},
	}

	markdown := formatTableListMarkdown(rows)
	if !strings.Contains(markdown, "aws_s3_bucket") || !strings.Contains(markdown, "Represents S3 buckets.") {
		t.Errorf("unexpected table list markdown: %q", markdown)
	}
}

func TestFormatTableShow(t *testing.T) {
	rows := []map[string]any{
		{
			"name":        "name",
			"type":        "text",
			"description": "Bucket name",
		},
		{
			"name":        "region",
			"type":        "text",
			"description": "AWS region",
		},
	}

	ddl := formatTableShow("aws_s3_bucket", rows)
	if !strings.Contains(ddl, "CREATE TABLE aws_s3_bucket") || !strings.Contains(ddl, "region text") || !strings.Contains(ddl, "Bucket name") {
		t.Errorf("unexpected table show schema formatting: %q", ddl)
	}
}
