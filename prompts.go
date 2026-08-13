package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// userMessage builds a single-turn prompt result.
func userMessage(desc, text string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{
		Description: desc,
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: text},
		}},
	}
}

func arg(name, desc string, required bool) *mcp.PromptArgument {
	return &mcp.PromptArgument{Name: name, Description: desc, Required: required}
}

// addPrompts registers guided read-only workflows. Each one names the tools and
// resources this server actually exposes, so the model is steered onto real
// capabilities rather than inventing calls.
func addPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "debug_cluster",
		Title:       "Investigate cluster health",
		Description: "Systematic read-only investigation of a cluster managed by Meshery.",
		Arguments: []*mcp.PromptArgument{
			arg("cluster_id", "Kubernetes server ID of the cluster to investigate. Omit to start from the connection list.", false),
			arg("symptoms", "What the user is observing, for example 'pods restarting in payments'.", false),
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		clusterID := req.Params.Arguments["cluster_id"]
		symptoms := req.Params.Arguments["symptoms"]

		var b strings.Builder
		b.WriteString("Investigate the health of a Kubernetes cluster through Meshery. Work read-only: this server exposes no tools that change cluster state.\n\n")
		if symptoms != "" {
			fmt.Fprintf(&b, "Reported symptoms: %s\n\n", symptoms)
		}
		b.WriteString("Suggested order:\n\n")
		if clusterID == "" {
			b.WriteString("1. Call meshery_list_kubernetes_connections to find the cluster and confirm its status is connected. A disconnected cluster explains stale data before anything else does.\n")
		} else {
			fmt.Fprintf(&b, "1. Confirm cluster %s is connected via meshery_list_kubernetes_connections. A disconnected cluster explains stale data before anything else does.\n", clusterID)
		}
		b.WriteString("2. Read meshery://meshsync/summary for a per-kind count, to see which resource kinds exist at all.\n")
		if clusterID != "" {
			fmt.Fprintf(&b, "3. Read meshery://clusters/%s/topology for the component graph and how workloads relate.\n", clusterID)
			fmt.Fprintf(&b, "4. Narrow to a namespace with meshery://clusters/%s/namespaces/{namespace}/workloads.\n", clusterID)
		} else {
			b.WriteString("3. Read meshery://clusters/{cluster_id}/topology for the component graph.\n")
			b.WriteString("4. Narrow to a namespace with meshery://clusters/{cluster_id}/namespaces/{namespace}/workloads.\n")
		}
		b.WriteString("\nTwo things to keep in mind while reasoning:\n\n")
		b.WriteString("- The topology resource reports an evaluated field. When it is false the relationship evaluation did not produce edges, which is not the same as the workloads having no relationships. Do not conclude a cluster is disconnected internally on that basis.\n")
		b.WriteString("- This data comes from MeshSync's discovered state, not a live read against the API server, so it lags. Say so when a conclusion depends on freshness.\n")
		b.WriteString("- Secrets are deliberately excluded from every response. Their absence is not evidence that none exist.\n")
		return userMessage("Read-only cluster investigation", b.String()), nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "review_design",
		Title:       "Review a Meshery design",
		Description: "Structured review of a design's component graph, focused on a chosen concern.",
		Arguments: []*mcp.PromptArgument{
			arg("design_id", "ID of the design to review.", true),
			arg("review_focus", "What to weight the review toward: security, cost, performance, or reliability.", false),
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		designID := req.Params.Arguments["design_id"]
		if designID == "" {
			return nil, fmt.Errorf("design_id is required")
		}
		focus := req.Params.Arguments["review_focus"]
		if focus == "" {
			focus = "reliability"
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Review Meshery design %s, weighting the review toward %s.\n\n", designID, focus)
		fmt.Fprintf(&b, "Start by reading meshery://designs/%s/topology, which returns the design's components as nodes and relationships as edges.\n\n", designID)
		b.WriteString("Structure the review as:\n\n")
		b.WriteString("1. What the design is: the components present, grouped by kind, and what the relationships say about how they connect.\n")
		fmt.Fprintf(&b, "2. Findings relevant to %s, each one tied to a specific component or relationship by name rather than stated in general terms.\n", focus)
		b.WriteString("3. What you could not assess from this data, stated plainly.\n\n")
		b.WriteString("The topology carries component identity and relationships, not full configuration. Where a judgement would need the component's configuration, say that rather than assuming a default.\n")
		return userMessage("Design review", b.String()), nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "compare_designs",
		Title:       "Compare two designs",
		Description: "Diff the component graphs of two designs.",
		Arguments: []*mcp.PromptArgument{
			arg("design_id_a", "First design ID, treated as the baseline.", true),
			arg("design_id_b", "Second design ID, treated as the candidate.", true),
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		a, b2 := req.Params.Arguments["design_id_a"], req.Params.Arguments["design_id_b"]
		if a == "" || b2 == "" {
			return nil, fmt.Errorf("design_id_a and design_id_b are both required")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Compare two Meshery designs, treating %s as the baseline and %s as the candidate.\n\n", a, b2)
		fmt.Fprintf(&b, "Read meshery://designs/%s/topology and meshery://designs/%s/topology.\n\n", a, b2)
		b.WriteString("Report:\n\n")
		b.WriteString("1. Components only in the baseline, only in the candidate, and in both. Match on component kind and display name, and say when a match is uncertain.\n")
		b.WriteString("2. Relationships added or removed, since a change in how components connect usually matters more than a change in their number.\n")
		b.WriteString("3. A one-line summary of what the candidate appears to change.\n\n")
		b.WriteString("Both graphs come from stored designs rather than live clusters, so this compares intent, not what is currently running.\n")
		return userMessage("Design comparison", b.String()), nil
	})
}
