package lsp

import (
	"encoding/json"
	"testing"
)

// ── Unit: parseWorkspaceEdits (modern documentChanges form) ─────────────────

func TestParseWorkspaceEditsDocumentChanges(t *testing.T) {
	// gopls >= 0.23 replies to rename with documentChanges, not changes.
	res := json.RawMessage(`{
		"documentChanges": [
			{
				"textDocument": {"version": 1, "uri": "file:///tmp/a.go"},
				"edits": [
					{"range": {"start": {"line": 2, "character": 5}, "end": {"line": 2, "character": 12}}, "newText": "newName"}
				]
			},
			{
				"textDocument": {"version": 1, "uri": "file:///tmp/b.go"},
				"edits": [
					{"range": {"start": {"line": 3, "character": 1}, "end": {"line": 3, "character": 8}}, "newText": "newName"}
				]
			}
		]
	}`)
	edits, err := parseWorkspaceEdits(res)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 2 {
		t.Fatalf("expected 2 edits from documentChanges, got %d: %+v", len(edits), edits)
	}
	if edits[0].Path != "/tmp/a.go" || edits[0].Line != 2 || edits[0].Col != 5 || edits[0].EndCol != 12 || edits[0].NewText != "newName" {
		t.Fatalf("unexpected first edit: %+v", edits[0])
	}
	if edits[1].Path != "/tmp/b.go" || edits[1].Line != 3 || edits[1].Col != 1 || edits[1].EndCol != 8 || edits[1].NewText != "newName" {
		t.Fatalf("unexpected second edit: %+v", edits[1])
	}
}

func TestParseApplyEditDocumentChanges(t *testing.T) {
	// workspace/applyEdit can also carry documentChanges.
	params := json.RawMessage(`{
		"label": "rename",
		"edit": {
			"documentChanges": [
				{
					"textDocument": {"version": 1, "uri": "file:///tmp/c.go"},
					"edits": [
						{"range": {"start": {"line": 0, "character": 0}, "end": {"line": 0, "character": 5}}, "newText": "Other"}
					]
				}
			]
		}
	}`)
	edits, err := parseApplyEdit(params)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 || edits[0].Path != "/tmp/c.go" || edits[0].NewText != "Other" {
		t.Fatalf("unexpected edits: %+v", edits)
	}
}
