#!/usr/bin/env bash
# Waymark Proxy: merge an upstream Zoraxy release into a new branch.
#
# Run from a full clone with "main" checked out. Creates upstream-merge/<tag>
# from main, merges the upstream release tag, keeps the paths Waymark
# removed removed, and notes the merge under "## Unreleased" in CHANGELOG.md.
# The Waymark version is not changed here: bump PRODUCT_VERSION
# (src/version_custom.go) when releasing. Exit codes: 0 merged,
# 2 conflicts, 3 nothing to merge.
#
# Environment: TAG (e.g. v3.3.6), UPSTREAM_URL (default upstream GitHub
# repo), FORK_REMOVED_PATHS (space separated). Writes results to
# $MERGE_RESULT_FILE (default merge-result.env).
set -euo pipefail

UPSTREAM_URL="${UPSTREAM_URL:-https://github.com/tobychui/zoraxy.git}"
FORK_REMOVED_PATHS="${FORK_REMOVED_PATHS:-docker docs example tools img .github/workflows/ci.yaml .github/workflows/docker.yml}"
# Files Waymark rewrote for its own branding: on a conflict, Waymark's version wins
OURS_PATHS="${OURS_PATHS:-README.md CHANGELOG.md CODEOWNERS .github/ISSUE_TEMPLATE src/web/img src/web/favicon.png}"
RESULT="${MERGE_RESULT_FILE:-merge-result.env}"

if ! [[ "${TAG:-}" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+(-rc[0-9]+)?$ ]]; then
  echo "::error::TAG must look like v3.3.6 or v3.3.6-rc1 (got '${TAG:-}')"
  exit 1
fi

BRANCH="upstream-merge/${TAG}"
UPSTREAM_REF="refs/upstream-tags/${TAG}"
git fetch --no-tags --quiet "$UPSTREAM_URL" "+refs/tags/${TAG}:${UPSTREAM_REF}"
upstream_sha="$(git rev-parse --short "${UPSTREAM_REF}^{commit}")"

old_base="$(sed -n -E 's/^[[:space:]]*SYSTEM_VERSION[[:space:]]*=[[:space:]]*"([0-9]+\.[0-9]+\.[0-9]+)".*/\1/p' src/def.go | head -n1)"
since_sha="$(git merge-base HEAD "${UPSTREAM_REF}^{commit}" 2>/dev/null | cut -c1-12 || true)"
git switch --quiet -C "$BRANCH"
if git merge-base --is-ancestor "${UPSTREAM_REF}^{commit}" HEAD; then
  echo "main already contains ${TAG}; nothing to merge."
  echo "STATUS=up-to-date" >"$RESULT"
  exit 3
fi

merge_failed=0
git merge --no-ff --no-commit --quiet "${UPSTREAM_REF}^{commit}" || merge_failed=1

# Paths Waymark removed stay removed: resolves modify/delete conflicts and
# drops files upstream added inside them
removed_list=""
for p in $FORK_REMOVED_PATHS; do
  if git ls-files --error-unmatch -- "$p" >/dev/null 2>&1 || [ -e "$p" ]; then
    git rm -r -q -f --ignore-unmatch -- "$p" >/dev/null
    rm -rf -- "$p"
    removed_list="${removed_list} ${p}"
  fi
done

# Branding files: keep Waymark's version where both sides changed them
kept_ours=""
for f in $(git diff --name-only --diff-filter=U); do
  for p in $OURS_PATHS; do
    if [ "$f" = "$p" ] || [[ "$f" == "$p"/* ]]; then
      git checkout --ours -- "$f" 2>/dev/null || git rm -q -- "$f"
      git add -- "$f" 2>/dev/null || true
      kept_ours="${kept_ours} ${f}"
    fi
  done
done
[ -n "$kept_ours" ] && echo "Kept Waymark's version of:${kept_ours}"

conflicts="$(git diff --name-only --diff-filter=U)"
if [ -n "$conflicts" ]; then
  echo "Merge conflicts in:"
  echo "$conflicts" | sed 's/^/  /'
  git merge --abort
  git switch --quiet main
  {
    echo "STATUS=conflicts"
    echo "UPSTREAM_SHA=${upstream_sha}"
    printf 'CONFLICTS=%s\n' "$(echo "$conflicts" | tr '\n' ' ')"
  } >"$RESULT"
  exit 2
fi
if [ "$merge_failed" = "1" ]; then
  echo "(merge stopped only on paths Waymark removed; kept them removed:${removed_list})"
fi

git commit --quiet -m "Merge upstream Zoraxy ${TAG}" -m "Paths removed in Waymark were kept removed:${removed_list:- none}"

# Note the merge in CHANGELOG.md under "## Unreleased" (created if missing)
base="$(sed -n -E 's/^[[:space:]]*SYSTEM_VERSION[[:space:]]*=[[:space:]]*"([0-9]+\.[0-9]+\.[0-9]+)".*/\1/p' src/def.go | head -n1)"
version="$(sed -n -E 's/^[[:space:]]*PRODUCT_VERSION[[:space:]]*=[[:space:]]*"([0-9]+\.[0-9]+\.[0-9]+)".*/\1/p' src/version_custom.go | head -n1)"
line="- Merged Zoraxy ${TAG} (\`${upstream_sha}\`, [release notes](https://github.com/tobychui/zoraxy/releases/tag/${TAG})). Waymark is now based on Zoraxy ${base}."
if grep -q '^## Unreleased' CHANGELOG.md; then
  # Add the line under Unreleased's "**Changed**" (creating that heading if needed)
  awk -v line="$line" '
    /^## / { if (inrel && !done) { print "**Changed**"; print line; print ""; done = 1 } inrel = ($0 ~ /^## Unreleased/) }
    { print }
    inrel && !done && /^\*\*Changed\*\*/ { print line; done = 1 }
    END { if (inrel && !done) { print ""; print "**Changed**"; print line } }
  ' CHANGELOG.md >CHANGELOG.md.new
else
  awk -v line="$line" '
    !done && /^## / { print "## Unreleased"; print ""; print "**Changed**"; print line; print ""; done = 1 }
    { print }
  ' CHANGELOG.md >CHANGELOG.md.new
fi
mv CHANGELOG.md.new CHANGELOG.md

git add CHANGELOG.md
git commit --quiet -m "Changelog: merged Zoraxy ${TAG}"

{
  echo "STATUS=merged"
  echo "BRANCH=${BRANCH}"
  echo "VERSION=${version}"
  echo "BASE=${base}"
  echo "OLD_BASE=${old_base}"
  echo "SINCE_SHA=${since_sha}"
  echo "UPSTREAM_SHA=${upstream_sha}"
  echo "REMOVED=${removed_list# }"
  echo "KEPT_OURS=${kept_ours# }"
} >"$RESULT"
echo "Merged ${TAG} into ${BRANCH} (Waymark ${version}, now based on Zoraxy ${base})."
if [ "$base" != "${TAG#v}" ]; then
  echo "::warning::Upstream ${TAG} reports SYSTEM_VERSION ${base}"
fi
