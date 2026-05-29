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

func TestMissingRelationName(t *testing.T) {
	err := errors.New("relation \"aws_ec2_load_balancer\" does not exist")
	got := missingRelationName(err)
	if got != "aws_ec2_load_balancer" {
		t.Fatalf("missingRelationName() = %q, expected %q", got, "aws_ec2_load_balancer")
	}
}

func TestRelationSearchCandidates_LoadBalancer(t *testing.T) {
	got := relationSearchCandidates("aws_ec2_load_balancer")
	if len(got) == 0 || got[0] != "aws_ec2_load_balancer" {
		t.Fatalf("expected first candidate to be the missing relation, got %#v", got)
	}

	has := func(s string) bool {
		for _, c := range got {
			if c == s {
				return true
			}
		}
		return false
	}

	if !has("load_balancer") {
		t.Fatalf("expected candidates to include %q, got %#v", "load_balancer", got)
	}
	if !has("ec2_load_balancer") {
		t.Fatalf("expected candidates to include %q, got %#v", "ec2_load_balancer", got)
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
