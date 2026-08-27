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

Meshery has several endpoints that answer a wrong request with `200 OK` and
nothing in it. `GET /api/system/meshsync/resources` filters with
`cluster_id IN (?)`, so a missing filter binds an empty slice and the cluster
reads as empty. Pagination is zero-based on both of Meshery's offset paths, so a
client that opens at page 1 skips the first page of every list. Most handlers
read only the lowercase `pagesize` and ignore `pageSize` without saying so. None
of these produce an error, and an AI client presents every one of them as an
answer.

A hand-written mock cannot catch any of them, because it returns the shape the
code under test expects. The mock agrees with the code, including where the code
is wrong about Meshery. The test passes, the reviewer sees green, and the failure
shows up the first time someone points the thing at a real server.

## What this is measured against

The claim above is checked rather than asserted. Each row applies one mutation
to this repository's Meshery client, then runs three suites against it: the
MCP-layer tests backed by a hand-written mock, the client tests, and the
fake-backed tests.

| Mutation applied to the client | Hand-written MCP mock | Client tests | This package |
|---|---|---|---|
| cluster filter dropped from the query | passes | catches | catches |
| pages requested one-based | passes | **passes** | catches |
| page size spelled `pageSize` not `pagesize` | passes | **passes** | catches |
| bearer header instead of the cookies | passes | catches | catches |

The two middle rows are the interesting ones: nothing in a suite written without
prior knowledge of the trap catches either. The other two are caught by a client
test only because a positive control for that exact query was hand-written after
the bug had already been found the hard way. This package makes those controls
one line each, so the next author does not have to know the trap first.

Reproduce with `./mutation_check.sh`.

## What it reproduces

Every behaviour below is taken from a handler in `meshery/meshery` at commit
`e6ed2de`, cited in `doc.go` next to the code that reproduces it.

**Authentication.** The two credentials are not symmetric.
`RemoteProvider.GetToken` reads `req.Cookie("token")` and nothing else, so an
`Authorization` header is not a session and neither is a token on the query
string. The provider selection has three channels: the `meshery-provider`
cookie, else an HTTP header of the same name, else `?provider=`. The fake
accepts all three for the provider and only the cookie for the token, and
`AssertAuthenticated` reports which channel carried it.

**Failing authentication is three behaviours, not one**, and which you get
depends on whether a token cookie was sent at all. With none,
`RemoteProvider.GetSession` returns `ErrEmptySession`, which `AuthMiddleware`
explicitly excludes from the `HandleUnAuthenticated` branch, so the request falls
through to `LoginHandler`: a non-GET gets a bare `404`, and a GET is redirected
off-host to the remote provider's own login URL. A token that is present but
invalid is the case that does reach `HandleUnAuthenticated`, which stays on
Meshery and redirects to `/auth/login` or `/provider`. Either way a
redirect-following client gets `200 OK` with HTML and fails inside its JSON
decoder, and an unauthenticated write looks like a missing endpoint.

The fake serves its own stand-in for the provider login page so tests stay
hermetic; `WithRemoteLoginURL` points it off-host if that hop is what you are
testing. `WithLocalProvider()` switches to the local provider's behaviour, which
accepts everything, because that is why a client with broken auth can pass its
whole suite against a locally started Meshery and fail against a remote one.

Two 401 paths are deliberately not reproduced, both documented in `doc.go`:
`HandleUnAuthenticated` counts attempts in a cookie and answers 401 once retries
reach `MaxAuthRetries` (3), and `AuthMiddleware` answers 401 outright on an
enforced-provider mismatch. Both are states a client reaches only after the ones
above.

**Cluster scoping.** `/resources` takes a JSON-encoded `clusterIds` array. Omit
it and the handler binds an empty slice into `cluster_id IN (?)`, which matches
no rows, so you get `200 OK` with nothing. Send a bare unquoted id instead and
`json.Unmarshal` fails, which is a 400. The silent case and the loud
case are different, and the fake keeps them different. Its sibling
`/resources/summary` takes a repeated singular `clusterId` and answers 400
without it, so the two spellings are not interchangeable either.

**Pagination.** Zero-based on both of Meshery's offset computations
(`offset = page * limit` and `offset := (page) * pageSize`). Negative pages are
clamped to 0, the default page size is 25, and `pageSize=all` skips the limit
entirely rather than falling back to the default.

**The page-size parameter is spelled differently per endpoint, silently.** Of
the endpoints this package serves, the contexts and designs handlers read
`q.Get("pagesize")` and ignore `pageSize`, while `getPaginationParams` and
`GetConnections` read `pageSize` and fall back to `pagesize`. A client sending
`pageSize` to the first group has it ignored and gets the default of 25 with no
error. Elsewhere in Meshery there are at least two further spellings,
`FetchSmiResult` reading only `pageSize` and the credentials handler reading
`page_size`, so this is a per-endpoint fact rather than a rule.

The fake reproduces the spelling each endpoint it serves actually reads rather
than accepting both everywhere, because accepting both is precisely how a mock
hides this. `AssertPageSizeSpelling` names the spelling that endpoint reads. The
lowercase spelling is safe on every endpoint the fake serves.

**Designs.** `patternFile` is a JSON *string* under a camelCase key. Decoding it
as a nested object, or reading only the older `pattern_file`, yields an empty
design and no error.

**Topology.** `?asDesign=true` empties the flat `resources` list and returns a
component graph instead, so the two are never both populated. Evaluation runs at
depth 1 and falls back to the un-evaluated design on failure while still
answering 200, which means an empty `relationships` array does not distinguish
"no edges" from "evaluation failed".

**Org scoping.** `/api/environments` answers 400 without an `orgId`, and
`/api/workspaces` answers 400 only when both `orgId` and the legacy `orgID` are
absent.

**Registry auth is a mix, not a blanket.** Every `GET` is registered with
`models.NoAuth` and needs no session, which is why a read-only client should not
be made to authenticate there. Several mutating routes are registered with
`models.ProviderAuth` and do need one, among them `POST /register`,
`DELETE /models/{id}`, `POST /relationships/evaluate` and the
connection-definition writes. `AssertAuthenticated` exempts the reads and not the
writes, because exempting the whole prefix would let an unauthenticated write
through.

Both prefixes count. `registerRegistryRoute` binds every registry subpath at
`/api/registry` and again at the deprecated `/api/meshmodels` alias, same handler
and same methods, so the fake serves both and the assertion exempts both. Demanding
cookies on the alias would fail a client that is doing nothing wrong, which is the
worst thing an assertion can do.

## Assertions

| Call | Catches |
|---|---|
| `AssertAuthenticated` | header auth, a token off the cookie, no provider on any channel |
| `AssertClusterScoped` | absent filter, bare id where an array is required, wrong spelling per endpoint |
| `AssertZeroBasedPaging` | a client that opens at page 1 |
| `AssertPageSizeSpelling` | `pageSize` sent where only `pagesize` is read |
| `AssertQuery` / `AssertNoQuery` | a dropped filter; a field that should never be requested |
| `AssertCalled` / `AssertNotCalled` | an endpoint skipped; a mutating route touched by a read-only server |

Assertions take a `mesherytest.T` interface rather than `*testing.T`, which is
how the package's own tests check that each one fires on a client that is wrong.
`*testing.T` satisfies it.

## Status

Stdlib-only, so it lifts into another repository as-is. Apache 2.0, matching
Meshery.
