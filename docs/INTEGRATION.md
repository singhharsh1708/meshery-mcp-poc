# Running the integration tests

These drive the compiled binary over stdio, as a real MCP client would, against
a real Meshery Server. Everything else in this repository runs against a fake,
which can only ever be as correct as its author's reading of the API. Several
things in that fake were wrong until a live server said otherwise.

```bash
make test-integration
```

They skip unless `MESHERY_URL` is set, so `go test ./...` stays hermetic.

## Getting a Meshery to test against

This is the part that stops people. Meshery publishes an amd64-only image, and
under emulation on arm64 it crashes during content seeding, which is why so much
MCP work against Meshery has only ever been tested against mocks.

Building the server from source works. Meshery's `go.mod` targets Go 1.26.4 and
the tree compiles natively:

```bash
git clone --depth 1 https://github.com/meshery/meshery.git
cd meshery/server/cmd && go build -o /tmp/meshery-server .

PORT=9081 PROVIDER=Local USE_GO_POLICY_ENGINE=true LOG_LEVEL=3 \
APP_PATH=./apps.json KEYS_PATH=../../server/permissions/keys.csv \
MESHSYNC_DEFAULT_DEPLOYMENT_MODE=operator /tmp/meshery-server
```

Or `make meshery-server MESHERY_SRC=/path/to/meshery`.

It seeds itself with roughly 355 designs and 292 models, which is enough to
exercise pagination and the design endpoints for real. `PROVIDER=Local` pins the
built-in local provider so no remote provider or network is needed.

## What they cover that a fake cannot

- **The design file encoding.** `GET /api/pattern` serves `patternFile` as YAML
  while `GET /api/pattern/{id}` serves the same field as JSON. A client handling
  only one of them fails here and passes everywhere else, because no mock author
  writes two encodings of one field.
- **Real payload volume.** 355 designs is past the first page, so a paging
  mistake shows up as missing data rather than as an identical small result.
- **Secret exclusion against real data**, rather than against the one Secret a
  fixture author remembered to include.
- **The tool surface a real client sees**, through an actual handshake with the
  compiled binary rather than an in-process server value.

## What they still do not cover

A real Kubernetes cluster. MeshSync needs an operator in-cluster, so the
cluster-scoped endpoints are exercised against their guards and their empty
shapes, not against discovered workloads. `TestClusterToolsDegradeHonestly`
checks that state reports itself honestly rather than looking like a healthy
empty cluster.
