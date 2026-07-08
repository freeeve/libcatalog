#!/bin/sh
# End-to-end run of the RoaringRange client browse path (tasks/158): builds the
# exampleSite with the roaringrange engine, emits the search/browse artifacts
# from the fixture catalog, serves over a Range-capable server (required by the
# reader; python http.server is not one), and drives it in headless Chromium.
# A second pass rebuilds under the minimal profile (disabled taxonomy/term
# kinds + shared sidebar) and drives the hydrated-sidebar facet UI (tasks/170).
#
# Needs: hugo, go, node, and Playwright with Chromium. If playwright is not
# npm-resolvable, point PLAYWRIGHT_PKG at an install, e.g. from the npx cache:
#   npx playwright install chromium
#   PLAYWRIGHT_PKG=$(ls -d ~/.npm/_npx/*/node_modules/playwright/index.js | head -1) ./run.sh
set -e
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
out=${TMPDIR:-/tmp}/lcat-e2e-$$
port=${PORT:-8510}
minport=$((port + 1))
trap 'kill $srv $minsrv 2>/dev/null || true; rm -rf "$out" "$out-min" "${out}-lcat"' EXIT

printf '[params.search]\n  engine = "roaringrange"\n' > "${out}.toml"
(cd "$repo/hugo/exampleSite" && hugo --quiet --config hugo.toml,"${out}.toml" --destination "$out")
rm "${out}.toml"

(cd "$repo" && go build -o "${out}-lcat" ./cmd/lcat)
"${out}-lcat" index --catalog "$here/fixture-catalog.json" --out "$out/search"

node "$here/range-server.mjs" "$out" "$port" &
srv=$!
sleep 1
node "$here/browse.spec.mjs" "http://127.0.0.1:$port"

# Minimal profile (tasks/157): no taxonomy/term pages, shared sidebar. The
# sidebar's unlinked rows must hydrate into reader toggles and replace the
# fallback panel (tasks/170), including the exclude toggles (tasks/173) --
# negatives is on in the exampleSite config, restated here so this pass never
# depends on the params merge strategy.
printf 'disableKinds = ["taxonomy","term"]\n[params.search]\n  engine = "roaringrange"\n[params.facets]\n  shared = true\n  negatives = true\n' > "${out}.toml"
(cd "$repo/hugo/exampleSite" && hugo --quiet --config hugo.toml,"${out}.toml" --destination "$out-min")
rm "${out}.toml"
cp -R "$out/search" "$out-min/search"

node "$here/range-server.mjs" "$out-min" "$minport" &
minsrv=$!
sleep 1
node "$here/browse-minimal.spec.mjs" "http://127.0.0.1:$minport"
