package contract

import (
	"strings"
	"testing"
)

func TestCompactValidationErrorPreservesAgentReadableReasons(t *testing.T) {
	validator, err := CompileInputSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text":  map[string]any{"type": "string"},
			"alias": map[string]any{"$ref": "#/$defs/nonEmptyString"},
			"user": map[string]any{
				"type":       "object",
				"required":   []string{"name"},
				"properties": map[string]any{"name": map[string]any{"type": "string"}},
			},
		},
		"required": []string{"text"},
		"$defs": map[string]any{
			"nonEmptyString": map[string]any{"type": "string", "minLength": 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		arguments map[string]any
		want      string
	}{
		{name: "missing root property", arguments: map[string]any{}, want: "$.text is required"},
		{name: "wrong property type", arguments: map[string]any{"text": 42}, want: "$.text must be string"},
		{name: "wrong referenced property type", arguments: map[string]any{"text": "ok", "alias": 42}, want: "$.alias must be string"},
		{name: "missing nested property", arguments: map[string]any{"text": "ok", "user": map[string]any{}}, want: "$.user.name is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validator.Validate(test.arguments)
			if err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
			if got := CompactValidationError(err); got != test.want {
				t.Fatalf("CompactValidationError() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCompileInputSchemaMakesSupportedDraftBoundaryExplicit(t *testing.T) {
	for _, draft := range []string{draft7HTTP, draft7HTTPS, draft2020URL} {
		if _, err := CompileInputSchema(map[string]any{
			"$schema": draft,
			"type":    "object",
		}); err != nil {
			t.Fatalf("CompileInputSchema(%q) error = %v", draft, err)
		}
	}

	for _, draft := range []string{
		"http://json-schema.org/draft-04/schema#",
		"http://json-schema.org/draft-06/schema#",
		"https://json-schema.org/draft/2019-09/schema",
	} {
		_, err := CompileInputSchema(map[string]any{
			"$schema": draft,
			"type":    "object",
		})
		if err == nil {
			t.Fatalf("CompileInputSchema(%q) unexpectedly succeeded", draft)
		}
		if !strings.Contains(err.Error(), "supported drafts are draft-07 and 2020-12") {
			t.Fatalf("CompileInputSchema(%q) error = %v", draft, err)
		}
	}
}
