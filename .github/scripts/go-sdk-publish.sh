#!/usr/bin/env bash
# Publishes an existing Go SDK version (tag sdk/go/vX.Y.Z) to the Go ecosystem
# and checks it the way a user would consume it:
#   1. sdk/go/go.mod declares the vanity module path;
#   2. the vanity host serves the go-import meta that maps the repository
#      prefix to this GitHub repository (sdk/go is a nested module in it);
#   3. proxy.golang.org resolves the version (this also makes it public);
#   4. a fresh module can `go get` and build against it;
#   5. pkg.go.dev is asked to index it.
#
# Usage: .github/scripts/go-sdk-publish.sh vX.Y.Z
set -euo pipefail

version="${1:?usage: go-sdk-publish.sh vX.Y.Z}"
prefix="go.vicoworks.com/agent-reliability-lab"
module="${prefix}/sdk/go"
repo="https://github.com/${GITHUB_REPOSITORY:-VicoAritonang/lab-of-night-and-flame}"
want="${prefix} git ${repo}"

[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "::error::invalid version '$version'"; exit 1; }

if ! grep -qx "module ${module}" sdk/go/go.mod; then
  echo "::error file=sdk/go/go.mod::module path must be ${module}"
  exit 1
fi

echo "Checking the vanity host: https://${module}?go-get=1"
served=""
for attempt in 1 2 3 4 5 6; do
  if body="$(curl -fsSL --max-time 20 "https://${module}?go-get=1")"; then
    served="$(printf '%s' "$body" | grep -o '<meta[^>]*"go-import"[^>]*>' || true)"
    printf '%s' "$served" | grep -qF "content=\"${want}\"" && break
  fi
  served="${served:-<no response>}"
  sleep 10
done
if ! printf '%s' "$served" | grep -qF "content=\"${want}\""; then
  cat <<EOF
::error::The vanity host does not serve the expected go-import meta.
Register the repository in the vanity worker (go-vanity src/packages.js):
  "${prefix#go.vicoworks.com/}": { repo: "${repo}", branch: "main" },
Expected :<meta name="go-import" content="${want}">
Served   : ${served}
EOF
  exit 1
fi
echo "ok: ${served}"

# Run outside the repository: its go.mod replaces the SDK with ./sdk/go, which
# would answer the query locally instead of proxy.golang.org.
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "Resolving ${module}@${version} through proxy.golang.org"
ok=""
for attempt in 1 2 3 4 5 6; do
  if (cd "$work" && GOWORK=off GOPROXY=https://proxy.golang.org go list -m "${module}@${version}"); then ok=1; break; fi
  sleep 20
done
[ -n "$ok" ] || { echo "::error::proxy.golang.org cannot resolve ${module}@${version}"; exit 1; }

echo "Consumer smoke test"
(
  cd "$work"
  go mod init example.com/consumer >/dev/null
  cat > main.go <<'GO'
package main

import arl "go.vicoworks.com/agent-reliability-lab/sdk/go"

func main() {
	lab := arl.New("smoke", arl.Options{})
	_ = lab.Enabled()
}
GO
  GOPROXY=https://proxy.golang.org go get "${module}@${version}"
  go build ./...
  go list -m "${module}"
)

curl -fsS -X POST "https://pkg.go.dev/fetch/${module}@${version}" >/dev/null || echo "::warning::pkg.go.dev fetch request failed; it will index the version on first visit"
echo "Published ${module}@${version}: go get ${module}@${version}"
