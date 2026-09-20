package main

import (
	"strings"
	"testing"
)

func TestValidateDiagram(t *testing.T) {
	for _, test := range []struct {
		name, block, want string
	}{
		{"flow", "flowchart LR\n    accTitle: Image path\n    accDescr: Verified content precedes execution.\n A --> B", ""},
		{"sequence", "sequenceDiagram\naccTitle: Launch\naccDescr: Parent releases child after setup.\n", ""},
		{"state", "stateDiagram-v2\naccTitle: Lifecycle\naccDescr: Legal transitions.\n", ""},
		{"missing title", "flowchart LR\naccDescr: Description\n", "accTitle"},
		{"empty title", "flowchart LR\naccTitle:   \naccDescr: Description\n", "accTitle"},
		{"missing description", "flowchart LR\naccTitle: Title\n", "accDescr"},
		{"empty description", "flowchart LR\naccTitle: Title\naccDescr: \t\n", "accDescr"},
		{"comment is not metadata", "flowchart LR\n%% accTitle: Not a title\naccDescr: Description\n", "accTitle"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateDiagram(test.block)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v; want %q", err, test.want)
			}
		})
	}
}

func TestMermaidBlocksInMixedDocument(t *testing.T) {
	document := "# Design\n```text\nRUNNING payload\n```\n```mermaid\nflowchart LR\naccTitle: First\naccDescr: A flow.\n```\n```mermaid\nstateDiagram-v2\naccTitle: Second\naccDescr: A state machine.\n```\n"
	blocks, err := mermaidBlocks(document)
	if err != nil || len(blocks) != 2 {
		t.Fatalf("blocks = %v, error = %v", blocks, err)
	}
	for _, block := range blocks {
		if err := validateDiagram(block); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mermaidBlocks("```mermaid\nflowchart LR\n"); err == nil {
		t.Fatal("unclosed diagram should fail")
	}
}
