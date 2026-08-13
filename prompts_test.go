package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/singhharsh1708/meshery-mcp-poc/meshery"
)

func promptSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	backend := mockTopologyMeshery(t)
	t.Cleanup(backend.Close)
	return connectTo(t, newServer(meshery.New(backend.URL, "tok", "prov")))
}

// TestPromptsAreListed checks the guided workflows appear in prompts/list with
// their declared arguments.
func TestPromptsAreListed(t *testing.T) {
	cs := promptSession(t)
	res, err := cs.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]*mcp.Prompt{}
	for _, p := range res.Prompts {
		found[p.Name] = p
	}
	for _, name := range []string{"debug_cluster", "review_design", "compare_designs"} {
		if found[name] == nil {
			t.Errorf("prompt %s not advertised", name)
		}
	}
	if p := found["review_design"]; p != nil {
		var required bool
		for _, a := range p.Arguments {
			if a.Name == "design_id" && a.Required {
				required = true
			}
		}
		if !required {
			t.Error("review_design should declare design_id as required")
		}
	}
}

// TestDebugClusterPromptUsesRealURIs checks the guidance names resources this
// server actually exposes, and warns about the ambiguous evaluated flag.
func TestDebugClusterPromptUsesRealURIs(t *testing.T) {
	cs := promptSession(t)
	res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{
		Name:      "debug_cluster",
		Arguments: map[string]string{"cluster_id": "c1", "symptoms": "pods restarting"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) == 0 {
		t.Fatal("no messages returned")
	}
	text := textOfContent(res.Messages[0].Content)
	for _, want := range []string{
		"meshery://clusters/c1/topology",
		"meshery_list_kubernetes_connections",
		"pods restarting",
		"evaluated",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt missing %q:\n%s", want, text)
		}
	}
}

// TestReviewDesignPromptRequiresID checks a missing required argument is an
// error rather than guidance with an empty ID interpolated into it.
func TestReviewDesignPromptRequiresID(t *testing.T) {
	cs := promptSession(t)
	if _, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{
		Name:      "review_design",
		Arguments: map[string]string{},
	}); err == nil {
		t.Fatal("expected an error when design_id is missing, got nil")
	}
}

// TestReviewDesignPromptDefaultsFocus checks the optional focus argument falls
// back rather than producing an empty instruction.
func TestReviewDesignPromptDefaultsFocus(t *testing.T) {
	cs := promptSession(t)
	res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{
		Name:      "review_design",
		Arguments: map[string]string{"design_id": "d1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := textOfContent(res.Messages[0].Content)
	if !strings.Contains(text, "reliability") {
		t.Errorf("expected a default review focus:\n%s", text)
	}
	if !strings.Contains(text, "meshery://designs/d1/topology") {
		t.Errorf("expected the design topology URI:\n%s", text)
	}
}

// TestCompareDesignsPromptNeedsBoth checks both IDs are required.
func TestCompareDesignsPromptNeedsBoth(t *testing.T) {
	cs := promptSession(t)
	if _, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{
		Name:      "compare_designs",
		Arguments: map[string]string{"design_id_a": "d1"},
	}); err == nil {
		t.Fatal("expected an error when design_id_b is missing, got nil")
	}
}

func textOfContent(c mcp.Content) string {
	if tc, ok := c.(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}
