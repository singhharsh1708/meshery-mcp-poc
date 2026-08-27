# mesherytest

A fake Meshery Server for testing Meshery clients, including MCP servers.

It is a real `httptest.Server` that reproduces Meshery's actual behaviour on the
endpoints an MCP server reads, quirks included, and then lets a test assert on
what the client sent rather than only on what came back.

```go
func TestMyTool(t *testing.T) {
	fake := mesherytest.New(t)
	client := myclient.New(fake.URL(), fake.Token, fake.Provider)

	out, err := client.ListResources(ctx, fake.Data().ClusterID())
	// ... assert on out ...

	fake.AssertAuthenticated(t)
	fake.AssertClusterScoped(t, "/api/system/meshsync/resources", fake.Data().ClusterID())
	fake.AssertZeroBasedPaging(t, "/api/pattern")
}
```

## Why

Meshery has several endpoints that answer a wrong request with `200 OK` and an
empty body. `GET /api/system/meshsync/resources` filters with `cluster_id IN
(?)`, so a missing filter is an empty `IN` clause and the cluster reads as
empty. Pagination is zero-based on both of Meshery's offset paths, so a client
that opens at page 1 skips the first page of every list. Neither produces an
error, and an AI client presents both as an answer.

A hand-written mock cannot catch either, because it returns the shape the code
under test expects. The mock agrees with the code, including where the code is
wrong about Meshery. The test passes, the reviewer sees green, and the failure
shows up the first time someone points the thing at a real server.

## What this is measured against

The claim above is checked rather than asserted. Each row applies one mutation
to this repository's Meshery client, then runs three suites against it: the
MCP-layer tests backed by a hand-written mock, the client tests, and the
fake-backed tests.

| Mutation applied to the client | Hand-written MCP mock | Client tests | This package |
|---|---|---|---|
| cluster filter dropped from the query | passes | catches | catches |
| pages requested one-based | passes | passes | catches |
| bearer header instead of the cookies | passes | catches | catches |

The middle row is the interesting one: nothing in a suite written without prior
knowledge of the trap catches it. The other two are caught by a client test only
because a positive control for that exact query was hand-written after the bug
was already known. This package makes those controls one line each, so the next
author does not have to know the trap first.

Reproduce with `./mutation_check.sh`.

## What it reproduces

Every behaviour below is taken from a handler in `meshery/meshery` at current
master, cited in `doc.go` next to the code that reproduces it.

**Authentication.** The session lives in the `token` and `meshery-provider`
cookies. `RemoteProvider.GetToken` reads `req.Cookie("token")` and nothing else,
so an `Authorization` header is not a session. The first unauthenticated call is
not answered with a 401 either: it is a 302 to a login page, and a client that
follows redirects gets `200 OK` with HTML and fails inside its JSON decoder.
`WithLocalProvider()` switches to the local provider's behaviour, which accepts
everything, because that is why a client with broken auth can pass its whole
suite against a locally started Meshery and fail against a remote one.

Two 401 paths exist and are deliberately not reproduced, both documented in
`doc.go`: Meshery counts attempts in a cookie and answers 401 once retries reach
`MaxAuthRetries` (3), and it answers 401 outright when an enforced provider key
does not match. Neither is what a client meets first, and reproducing the retry
counter would make the fake stateful for no gain.

**Cluster scoping.** `/resources` takes a JSON-encoded `clusterIds` array. Omit
it and the handler sets the filter to an empty slice, so the SQL becomes
`cluster_id IN ()` and you get `200 OK` with nothing. Send a bare unquoted id
instead and `json.Unmarshal` fails, which is a 400. The silent case and the loud
case are different, and the fake keeps them different. Its sibling
`/resources/summary` takes a repeated singular `clusterId` and answers 400
without it, so the two spellings are not interchangeable either.

**Pagination.** Zero-based on both of Meshery's offset computations
(`offset = page * limit` and `offset := (page) * pageSize`). Negative pages are
clamped to 0, the default page size is 25, `pageSize` is canonical with
`pagesize` accepted as the legacy spelling, and `pageSize=all` skips the limit
entirely rather than falling back to the default.

**Designs.** `patternFile` is a JSON *string* under a camelCase key. Decoding it
as a nested object, or reading only the older `pattern_file`, yields an empty
design and no error.

**Topology.** `?asDesign=true` clears the flat `resources` list and returns a
component graph instead. You get the graph or the list, never both.

**Org scoping.** `/api/environments` and `/api/workspaces` answer 400 without an
`orgId`.

**Registry auth is a mix, not a blanket.** Every `GET` under `/api/registry` is
registered with `models.NoAuth` and needs no session, which is why a read-only
client should not be made to authenticate there. Several mutating routes are
registered with `models.ProviderAuth` and do need one, among them
`POST /api/registry/register`, `DELETE /api/registry/models/{id}`,
`POST /api/registry/relationships/evaluate` and the connection-definition
writes. `AssertAuthenticated` exempts the reads and not the writes, because
exempting the whole prefix would let an unauthenticated write through.

## Assertions

| Call | Catches |
|---|---|
| `AssertAuthenticated` | header auth, missing provider cookie |
| `AssertClusterScoped` | absent filter, bare id where an array is required, wrong spelling per endpoint |
| `AssertZeroBasedPaging` | a client that opens at page 1 |
| `AssertQuery` / `AssertNoQuery` | a dropped filter; a field that should never be requested |
| `AssertCalled` / `AssertNotCalled` | an endpoint skipped; a mutating route touched by a read-only server |

Assertions take a `mesherytest.T` interface rather than `*testing.T`, which is
how the package's own tests check that each one fires on a client that is wrong.
`*testing.T` satisfies it.

## Status

Written for [meshery-extensions/meshery-mcp-server](https://github.com/meshery-extensions/meshery-mcp-server)
and kept here until there is somewhere upstream to put it. Apache 2.0, matching
Meshery.
