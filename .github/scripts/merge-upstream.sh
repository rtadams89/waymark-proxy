#!/usr/bin/env bash
# Custom fork: merge an upstream Zoraxy release into a new branch.
#
# Run from a full clone of the fork with "custom" checked out. Creates
# upstream-merge/<tag> from custom, merges the upstream release tag, keeps
# the paths this fork removed removed, resets FORK_REVISION to 1 and adds a
# CHANGELOG-custom.md entry. Exit codes: 0 merged, 2 conflicts, 3 nothing
# to merge.
#
# Environment: TAG (e.g. v3.3.6), UPSTREAM_URL (default upstream GitHub
# repo), FORK_REMOVED_PATHS (space separated). Writes results to
# $MERGE_RESULT_FILE (default merge-result.env).
set -euo pipefail

UPSTREAM_URL="${UPSTREAM_URL:-https://github.com/tobychui/zoraxy.git}"
FORK_REMOVED_PATHS="${FORK_REMOVED_PATHS:-docker docs example tools .github/workflows/ci.yaml .github/workflows/docker.yml}"
RESULT="${MERGE_RESULT_FILE:-merge-result.env}"

if ! [[ "${TAG:-}" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "::error::TAG must look like v3.3.6 (got '${TAG:-}')"
  exit 1
fi

BRANCH="upstream-merge/${TAG}"
UPSTREAM_REF="refs/upstream-tags/${TAG}"
git fetch --no-tags --quiet "$UPSTREAM_URL" "+refs/tags/${TAG}:${UPSTREAM_REF}"
upstream_sha="$(git rev-parse --short "${UPSTREAM_REF}^{commit}")"

old_base="$(sed -n -E 's/^[[:space:]]*SYSTEM_VERSION[[:space:]]*=[[:space:]]*"([0-9]+\.[0-9]+\.[0-9]+)".*/\1/p' src/def.go | head -n1)"
git switch --quiet -C "$BRANCH"
if git merge-base --is-ancestor "${UPSTREAM_REF}^{commit}" HEAD; then
  echo "custom already contains ${TAG}; nothing to merge."
  echo "STATUS=up-to-date" >"$RESULT"
  exit 3
fi

merge_failed=0
git merge --no-ff --no-commit --quiet "${UPSTREAM_REF}^{commit}" || merge_failed=1

# Paths this fork removed stay removed: resolves modify/delete conflicts and
# drops files upstream added inside them
removed_list=""
for p in $FORK_REMOVED_PATHS; do
  if git ls-files --error-unmatch -- "$p" >/dev/null 2>&1 || [ -e "$p" ]; then
    git rm -r -q -f --ignore-unmatch -- "$p" >/dev/null
    rm -rf -- "$p"
    removed_list="${removed_list} ${p}"
  fi
done

conflicts="$(git diff --name-only --diff-filter=U)"
if [ -n "$conflicts" ]; then
  echo "Merge conflicts in:"
  echo "$conflicts" | sed 's/^/  /'
  git merge --abort
  git switch --quiet custom
  {
    echo "STATUS=conflicts"
    echo "UPSTREAM_SHA=${upstream_sha}"
    printf 'CONFLICTS=%s\n' "$(echo "$conflicts" | tr '\n' ' ')"
  } >"$RESULT"
  exit 2
fi
if [ "$merge_failed" = "1" ]; then
  echo "(merge stopped only on paths this fork removed; kept them removed:${removed_list})"
fi

git commit --quiet -m "Merge upstream Zoraxy ${TAG}" -m "Paths removed in this fork were kept removed:${removed_list:- none}"

# Start the new upstream base at revision 1
base="$(sed -n -E 's/^[[:space:]]*SYSTEM_VERSION[[:space:]]*=[[:space:]]*"([0-9]+\.[0-9]+\.[0-9]+)".*/\1/p' src/def.go | head -n1)"
sed -i -E 's/^([[:space:]]*FORK_REVISION[[:space:]]*=[[:space:]]*)"[0-9]+"/\1"1"/' src/version_custom.go
version="${base}-custom.1"

entry="$(mktemp)"
cat >"$entry" <<EOF
## ${version}

Base: upstream ${TAG} (\`${upstream_sha}\`).

**Changed**
- Merged upstream Zoraxy ${TAG} ([release notes](https://github.com/tobychui/zoraxy/releases/tag/${TAG})). All fork changes are carried over; the fork revision restarts at 1.

EOF
awk -v entry="$entry" '
  !done && /^## / { while ((getline line < entry) > 0) print line; done = 1 }
  { print }
  END { if (!done) { while ((getline line < entry) > 0) print line } }
' CHANGELOG-custom.md >CHANGELOG-custom.md.new
mv CHANGELOG-custom.md.new CHANGELOG-custom.md

git add src/version_custom.go CHANGELOG-custom.md
git commit --quiet -m "Start ${version}"

{
  echo "STATUS=merged"
  echo "BRANCH=${BRANCH}"
  echo "VERSION=${version}"
  echo "BASE=${base}"
  echo "OLD_BASE=${old_base}"
  echo "UPSTREAM_SHA=${upstream_sha}"
  echo "REMOVED=${removed_list# }"
} >"$RESULT"
echo "Merged ${TAG} into ${BRANCH} as ${version}."
if [ "$base" != "${TAG#v}" ]; then
  echo "::warning::Upstream ${TAG} reports SYSTEM_VERSION ${base}"
fi
