// Package mesheryfake provides a fake Meshery Server for testing MCP tools and
// clients.
//
// It exists because Meshery has a handful of behaviours that a hand-written
// mock will not reproduce unless you already know about them, and every one of
// them fails by returning plausible data rather than an error. A test whose mock
// returns the shape the code under test expects will pass while the same code
// returns nothing against a real instance.
//
// The behaviours reproduced here, each verified against meshery/meshery at
// master and cited in the file that implements it:
//
//   - Pagination is zero-based. offset = page * pageSize, so page=1 skips the
//     first page rather than returning it.
//   - GET /api/system/meshsync/resources filters on clusterIds, a JSON-encoded
//     array in a single parameter. Absent, it matches no rows and answers 200
//     with an empty list.
//   - GET /api/system/meshsync/resources/summary requires a repeated singular
//     clusterId and answers 400 without one. The two sibling endpoints disagree
//     on both spelling and encoding.
//   - Authentication is the token and meshery-provider cookie pair. An
//     unauthenticated request is redirected to a login page with a 302, not
//     answered with 401, so a client that follows redirects fails in its JSON
//     decoder rather than reporting an auth problem.
//   - A design's file is a JSON string under patternFile. Older releases spelled
//     it pattern_file.
//   - List envelopes use camelCase: page, pageSize, totalCount.
//   - /api/environments and /api/workspaces require orgId and answer 400
//     without it.
//   - /api/registry/* is unauthenticated.
//
// Typical use:
//
//	srv := mesheryfake.New(t)
//	defer srv.Close()
//	client := yourclient.New(srv.URL(), srv.Token, srv.Provider)
//
//	if _, err := client.ListDesigns(ctx); err != nil {
//		t.Fatal(err)
//	}
//	srv.AssertAuthenticated(t)
//	srv.AssertQuery(t, "/api/pattern", "page", "0")
package mesheryfake
