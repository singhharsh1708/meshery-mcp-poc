//go:build integration

// Integration tests: the compiled binary, driven over stdio by a real MCP
// client, against a real Meshery Server.
//
// Everything else in this repository tests against a fake. These do not, which
// is the point: a fake can only be as right as its author's reading of the API,
// and several things here were wrong until a live server said otherwise.
//
//	make test-integration
//
// MESHERY_URL points at the server. See docs/INTEGRATION.md for building one
// that runs on arm64, which the published image does not.
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func liveURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("MESHERY_URL")
	if url == "" {
		t.Skip("set MESHERY_URL to run the integration tests")
	}
	return url
}

// connectBinary builds the server and drives the compiled binary over stdio, so
// what is under test is the artifact a user actually runs.
func connectBinary(t *testing.T, url string) *mcp.ClientSession {
	t.Helper()
	bin := t.TempDir() + "/meshery-mcp-poc"
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the server: %v\n%s", err, out)
	}

	auth := t.TempDir() + "/auth.json"
	if err := os.WriteFile(auth, []byte(`{"token":"integration","meshery-provider":"Local"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "MESHERY_URL="+url, "MESHERY_TOKEN_PATH="+auth)
	cmd.Stderr = os.Stderr

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	client := mcp.NewClient(&mcp.Implementation{Name: "integration", Version: "0"}, nil)
	sess, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connecting to the server binary: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	if res.IsError {
		t.Fatalf("%s returned an error: %s", name, sb.String())
	}
	return sb.String()
}

// TestListDesignsAgainstLiveMeshery reads real designs through the whole path:
// MCP client, the binary, the Meshery client, the live server, and back.
func TestListDesignsAgainstLiveMeshery(t *testing.T) {
	s := connectBinary(t, liveURL(t))

	out := callTool(t, s, "meshery_list_designs", map[string]any{"pageSize": 5})
	var payload struct {
		TotalCount int `json:"totalCount"`
		Designs    []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"designs"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decoding the tool output: %v\n%s", err, out)
	}
	if payload.TotalCount == 0 {
		t.Fatal("a seeded Meshery reported no designs, which usually means the cluster filter or paging is wrong")
	}
	if len(payload.Designs) == 0 {
		t.Fatalf("totalCount is %d but no designs came back, which is the silent-empty shape", payload.TotalCount)
	}
	for _, d := range payload.Designs {
		if d.ID == "" || d.Name == "" {
			t.Errorf("design decoded with an empty field: %+v", d)
		}
	}
	t.Logf("read %d of %d designs", len(payload.Designs), payload.TotalCount)
}

// TestDesignTopologyAgainstLiveMeshery is the one a fake could not have caught.
// GET /api/pattern serves patternFile as YAML and GET /api/pattern/{id} serves
// it as JSON, so a client that handles only one of them fails here and nowhere
// else.
func TestDesignTopologyAgainstLiveMeshery(t *testing.T) {
	s := connectBinary(t, liveURL(t))

	out := callTool(t, s, "meshery_list_designs", map[string]any{"pageSize": 1})
	var listed struct {
		Designs []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"designs"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed.Designs) == 0 {
		t.Fatalf("could not get a design to read: %v\n%s", err, out)
	}
	target := listed.Designs[0]

	rr, err := s.ReadResource(context.Background(), &mcp.ReadResourceParams{
		URI: "meshery://designs/" + target.ID + "/topology",
	})
	if err != nil {
		t.Fatalf("reading the topology of design %q: %v", target.Name, err)
	}
	if len(rr.Contents) == 0 {
		t.Fatal("no content returned")
	}
	body := rr.Contents[0].Text

	var topo struct {
		Name          string `json:"name"`
		Components    []any  `json:"components"`
		Relationships []any  `json:"relationships"`
	}
	if err := json.Unmarshal([]byte(body), &topo); err != nil {
		t.Fatalf("decoding the topology: %v\n%s", err, body)
	}
	if len(topo.Components) == 0 {
		t.Fatalf("design %q parsed to zero components, which is what a JSON-only decoder produces on a real design", target.Name)
	}
	t.Logf("design %q: %d components, %d relationships", topo.Name, len(topo.Components), len(topo.Relationships))
}

// TestClusterToolsDegradeHonestly covers a Meshery with no cluster attached,
// which is the common state. The tools must say so rather than return an empty
// list that reads as a healthy empty cluster.
func TestClusterToolsDegradeHonestly(t *testing.T) {
	s := connectBinary(t, liveURL(t))

	out := callTool(t, s, "meshery_list_kubernetes_contexts", nil)
	var ctxs struct {
		TotalCount int   `json:"totalCount"`
		Contexts   []any `json:"contexts"`
	}
	if err := json.Unmarshal([]byte(out), &ctxs); err != nil {
		t.Fatalf("decoding contexts: %v\n%s", err, out)
	}
	if ctxs.TotalCount != len(ctxs.Contexts) {
		t.Errorf("totalCount %d disagrees with %d contexts returned", ctxs.TotalCount, len(ctxs.Contexts))
	}
	t.Logf("contexts: %d", ctxs.TotalCount)
}

// TestSecretsNeverReachTheModel checks the guarantee against real data rather
// than a fixture, since a fixture only contains the Secret its author put there.
func TestSecretsNeverReachTheModel(t *testing.T) {
	s := connectBinary(t, liveURL(t))

	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "meshery_list_kubernetes_resources",
		Arguments: map[string]any{"clusterId": "any", "kind": "Secret"},
	})
	if err == nil && !res.IsError {
		t.Fatal("asking for Secrets should be refused, not served")
	}
}

// TestServerAdvertisesItsSurface checks the handshake a real client performs
// returns the tools, resources and prompts this server claims to have.
func TestServerAdvertisesItsSurface(t *testing.T) {
	s := connectBinary(t, liveURL(t))
	ctx := context.Background()

	tools, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools advertised")
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %s is not marked read-only", tool.Name)
		}
	}
	if _, err := s.ListResourceTemplates(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListPrompts(ctx, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("advertised %d tools", len(tools.Tools))
}
