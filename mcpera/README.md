# mcpera

Reports which MCP protocol era a server actually serves, by talking to it.

```bash
go build -o mcpera ./cmd/mcpera
./mcpera ./my-mcp-server
```

## Why

Revision `2026-07-28` is the first breaking change to MCP. It removes the
`initialize` handshake, makes every request carry its own version in `_meta`,
and adds `server/discover`. That splits the world into a legacy era
(`2025-11-25` and earlier) and a modern one, and the specification's own
compatibility table is blunt about the crossing:

> **Modern client, Legacy server.** Fails. The server may reject the request
> with an implementation-defined error, stay silent, or even process an
> era-ambiguous method under legacy semantics.

The third outcome is the one worth detecting, because it does not look like a
failure. A legacy server can answer a modern request by running it and returning
a legacy-shaped result: no error, no version acknowledgement, nothing on the wire
saying the eras did not match. The client believes it negotiated `2026-07-28`.
The server never negotiated anything.

The only signal is the shape of the result. Revision `2026-07-28` puts
`resultType` on a result and `io.modelcontextprotocol/serverInfo` in its `_meta`;
the legacy body has neither. A client that reads `content` and stops, which is
most of them, cannot tell.

## Measured

Four servers, each a one-tool stdio server built against a different SDK, probed
with a request declaring `protocolVersion: 2026-07-28` and the
`clientCapabilities` the revision requires:

| server | `initialize` | `server/discover` | modern request | result shape | era |
|---|---|---|---|---|---|
| `mark3labs/mcp-go` v0.57.0 | yes | `-32601` | **runs it** | **legacy** | legacy |
| `mark3labs/mcp-go` v0.58.0 | yes | `-32601` | **runs it** | **legacy** | legacy |
| `mark3labs/mcp-go` v1.0.0-beta.1 | yes | yes | runs it | modern | dual-era |
| `modelcontextprotocol/go-sdk` v1.7.0 | yes | yes | runs it | modern | dual-era |

Raw, for the row that matters:

```text
request  {"method":"tools/call","params":{"name":"ping","arguments":{},
          "_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28", ...}}}

v0.58.0  {"result":{"content":[{"type":"text","text":"pong"}]}}
v1.7.0   {"result":{"_meta":{"io.modelcontextprotocol/serverInfo":{...}},
                    "content":[{"type":"text","text":"pong"}],"resultType":"complete"}}
```

v0.58.0 executed the tool for a client it never handshook with, and answered in
the old shape.

The spec's own mitigation holds up: it tells stdio clients to send
`server/discover` first so the mismatch fails deterministically, and on v0.58.0
that probe does fail cleanly with `-32601`. The hazard is only for a client that
skips it.

## Reading the output

`era` is one of `legacy`, `modern`, `dual-era` or `unknown`. Dual-era is the only
posture that works with every client: a modern client cannot fall back to a
legacy server, and a legacy client has no fall-forward mechanism at all.

The command exits non-zero when it finds a silent downgrade, so it can gate CI.

## Scope

stdio only. The Streamable HTTP era negotiation runs through the
`MCP-Protocol-Version` header and its own server-validation rules, which this
does not probe yet.

The rows above are one-tool servers, not the SDKs in full. They say what
these SDKs do on a default stdio server, which is what an MCP server author gets
by following each SDK's README.
