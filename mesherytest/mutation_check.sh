#!/usr/bin/env bash
set -uo pipefail
cd "$(dirname "$0")/.."

CLIENT=meshery/client.go
BACKUP=$(mktemp)
E2E=fake_e2e_test.go
E2E_STASH=$(mktemp -d)/$E2E
restore() { cp "$BACKUP" "$CLIENT"; [ -f "$E2E_STASH" ] && mv "$E2E_STASH" "$E2E"; rm -f "$BACKUP"; }
trap restore EXIT INT TERM
cp "$CLIENT" "$BACKUP"

verdict() { go test "$@" >/dev/null 2>&1 && echo "passes" || echo "catches"; }

printf '%-38s %-22s %-14s %s\n' "mutation" "hand-written MCP mock" "client tests" "mesherytest"
printf '%s\n' "--------------------------------------------------------------------------------------"

for mutation in cluster-filter one-based-paging bearer-header pagesize-spelling; do
  cp "$BACKUP" "$CLIENT"
  case "$mutation" in
    cluster-filter)
      perl -0pi -e 's/(func setClusterIDs\(q url\.Values, clusterIDs \.\.\.string\) error \{)/$1\n\tif true { return nil }/' "$CLIENT" ;;
    one-based-paging)
      perl -0pi -e 's/(func \(c \*Client\) ListKubernetesResources\([^)]*\) \(\*MeshSyncResponse, error\) \{)/$1\n\tpage = page + 1/' "$CLIENT" ;;
    bearer-header)
      perl -0pi -e 's/\treq\.AddCookie\(&http\.Cookie\{Name: "token".*\n\treq\.AddCookie\(&http\.Cookie\{Name: "meshery-provider".*\n/\treq.Header.Set("Authorization", "Bearer "+c.token)\n/' "$CLIENT" ;;
    pagesize-spelling)
      perl -0pi -e 's/q\.Set\("pagesize", strconv\.Itoa\(pageSize\)\)/q.Set("pageSize", strconv.Itoa(pageSize))/' "$CLIENT" ;;
  esac

  mv "$E2E" "$E2E_STASH"
  mcp=$(verdict .)
  client=$(verdict ./meshery/)
  mv "$E2E_STASH" "$E2E"
  fake=$(verdict . ./mesherytest/ -run 'AgainstFakeMeshery|SatisfiesEveryAssertion')

  printf '%-38s %-22s %-14s %s\n' "$mutation" "$mcp" "$client" "$fake"
done

cp "$BACKUP" "$CLIENT"
