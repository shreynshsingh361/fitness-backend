#!/usr/bin/env bash
# Goes back to the version that was running before the last deploy:  ./scripts/rollback.sh
#
# deploy.sh writes the commit it is leaving to .previous. This checks that commit out and deploys
# it again. Database changes are not undone: migrations only go forward, and a new migration only
# adds things the old code ignores. If a deploy damaged data, use scripts/restore.sh instead.
set -euo pipefail
cd "$(dirname "$0")/.."

[ -d .git ] || { echo "not a git checkout: nothing to roll back to" >&2; exit 1; }
[ -s .previous ] || { echo "no .previous file: the last deploy did not record a version" >&2; exit 1; }
target="$(cat .previous)"
echo "Going back to $target"
git checkout --detach "$target"
./scripts/deploy.sh --no-pull
echo "Rolled back. Run  git checkout main  and fix the problem before the next deploy."
