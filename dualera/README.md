# dualera

Makes a legacy MCP server answer modern clients, without touching it.

```bash
go build -o dualera ./cmd/dualera
dualera ./your-legacy-mcp-server
```

## The problem

Revision `2026-07-28` removed the `initialize` handshake. A server built before
it answers `initialize` and nothing else, and the specification's compatibility
table says a modern client meeting one **fails**, possibly by having its request
run under legacy semantics with neither side noticing. The only posture that
works with every client is dual-era, answering both openings.

Getting there normally means upgrading the SDK. In Go that is `mcp-go`
v1.0.0-beta.1, a beta and a v1 API break from the 0.x line that essentially every
existing server is on. So the servers that most need to become dual-era are the
ones for which the upgrade is most expensive.

This is the other route. The bridge speaks both eras to the client, holds one
legacy session open to the server it wraps, and translates.

## Measured

Same binary, an `mcp-go` v0.57.0 server, before and after. Measured with
[`mcpera`](../mcpera), which was written first and knows nothing about this:

```text
$ mcpera ./legacy-server
era: legacy
  initialize          yes  (2025-11-25)
  server/discover     no  (-32601: Method server/discover not found)
  modern request      yes
  modern result shape no
  Ran a request declaring protocolVersion 2026-07-28 and answered in the legacy
  shape, with no error and no version acknowledgement.
exit 1

$ mcpera ./dualera ./legacy-server
era: dual-era
  initialize          yes  (2025-11-25)
  server/discover     yes
  modern request      yes
  modern result shape yes
  Serves both eras, which is the only posture that works with every client.
exit 0
```

End to end through the bridge, against a server that has never heard of
`2026-07-28`:

```text
modern client, no handshake
  tools/list  -> ['ping']           resultType: complete
  tools/call  -> pong               resultType: complete, serverInfo present

legacy client, initialize first
  initialize  -> 2025-11-25         serverInfo: legacy-probe
  tools/call  -> pong               no resultType, the legacy shape it expects

server/discover
  supportedVersions: ['2026-07-28', '2025-11-25', '2025-06-18']
  capabilities:      {"tools":{}}   from the wrapped server's own handshake
```

## What it does

- `initialize` is passed straight through, so a legacy client is served exactly
  as before and does not get modern markers it would not understand.
- `server/discover` is answered from the capabilities, server info and
  instructions the wrapped server reported during the bridge's own handshake, so
  a modern client sees what that server really advertises rather than a guess.
- A request carrying a modern `protocolVersion` in `_meta` is forwarded onto the
  established session, and its result comes back with `resultType` and
  `serverInfo`. Those two markers are the only thing on the wire distinguishing
  a modern peer from a silent downgrade, which is the whole point.
- Errors from the wrapped server are passed through as errors. Turning a failure
  into a success would be the same class of bug this exists to remove.
- The bridge's own handshake runs under request ids from a reserved high range,
  so a client using small integer ids never sees a reply meant for the bridge.

## Scope

stdio. The bridge is a filter between two pipes, so it composes with anything
that speaks MCP over stdio.

It does not make a legacy server genuinely stateless. There is one session
underneath, shared by every modern request, which is the pragmatic trade: modern
clients get served correctly by a server that cannot be stateless, rather than
not served at all. A server with per-session state that modern clients expect to
be isolated wants a real upgrade, not this.

Nor does it add capabilities the wrapped server lacks. `subscriptions/listen` and
the rest of the modern surface are only there if the server underneath has them.
