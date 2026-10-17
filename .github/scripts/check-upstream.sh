#!/usr/bin/env bash
# Custom fork: report a new upstream Zoraxy release.
#
# Upstream publishes release candidates (vX.Y.Z-rcN) as normal releases
# before the final vX.Y.Z, so this follows final releases only and mentions
# a newer release candidate in the run summary.
#
# A final release counts as new when its version is at least the upstream
# version this branch is based on (SYSTEM_VERSION in src/def.go) and its
# commit is not already merged into this branch. Then it opens an issue
# labelled "upstream-release" (once per release) and starts the "Merge
# upstream release" workflow, which prepares a pull request.
#
# Needs a full clone (fetch-depth: 0). Environment: GH_TOKEN,
# GITHUB_REPOSITORY, GITHUB_STEP_SUMMARY (set by Actions), UPSTREAM_REPO
# (default tobychui/zoraxy), UPSTREAM_URL (git URL, default from
# UPSTREAM_REPO), DRY_RUN=1 to only print.
set -euo pipefail

UPSTREAM_REPO="${UPSTREAM_REPO:-tobychui/zoraxy}"
UPSTREAM_URL="${UPSTREAM_URL:-https://github.com/${UPSTREAM_REPO}.git}"
SUMMARY="${GITHUB_STEP_SUMMARY:-/dev/stdout}"

base="$(sed -n -E 's/^[[:space:]]*SYSTEM_VERSION[[:space:]]*=[[:space:]]*"([0-9]+\.[0-9]+\.[0-9]+)".*/\1/p' src/def.go | head -n1)"
if [ -z "$base" ]; then
  echo "::error::Could not read SYSTEM_VERSION from src/def.go"
  exit 1
fi

# Highest version among tags matching a pattern ("" if none)
newest_tag() {
  grep -E "$1" | awk '{v=$0; sub(/^v/, "", v); print v " " $0}' | sort -V -k1,1 | tail -n1 | cut -d' ' -f2
}
version_ge() { # $1 >= $2
  [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n1)" = "$1" ]
}

releases_json="$(gh api "repos/${UPSTREAM_REPO}/releases?per_page=50")"
tags="$(jq -r '.[] | select(.draft | not) | .tag_name' <<<"$releases_json")"
tag="$(newest_tag '^v?[0-9]+\.[0-9]+\.[0-9]+$' <<<"$tags" || true)"
rc_tag="$(newest_tag '^v?[0-9]+\.[0-9]+\.[0-9]+-rc[0-9]+$' <<<"$tags" || true)"

if [ -z "$tag" ]; then
  echo "::warning::No final upstream release found"
  exit 0
fi
latest="${tag#v}"

note_rc() {
  if [ -n "$rc_tag" ]; then
    rc_version="${rc_tag#v}"
    rc_version="${rc_version%%-rc*}"
    if ! version_ge "$latest" "$rc_version" && version_ge "$rc_version" "$base"; then
      echo "Release candidate ${rc_tag} is out; waiting for its final release." | tee -a "$SUMMARY"
    fi
  fi
}

if ! version_ge "$latest" "$base"; then
  echo "Up to date: this fork is based on upstream ${base}; the latest final upstream release is ${tag}." | tee -a "$SUMMARY"
  note_rc
  exit 0
fi

# Same or newer version: new only if its commit is not merged yet
git fetch --no-tags --quiet "$UPSTREAM_URL" "+refs/tags/${tag}:refs/upstream-tags/${tag}"
if git merge-base --is-ancestor "refs/upstream-tags/${tag}^{commit}" HEAD; then
  echo "Up to date: this branch already includes upstream ${tag}." | tee -a "$SUMMARY"
  note_rc
  exit 0
fi

since="$(git merge-base HEAD "refs/upstream-tags/${tag}^{commit}" 2>/dev/null | cut -c1-12 || true)"
echo "Upstream ${tag} is available (this fork is based on ${base})." | tee -a "$SUMMARY"
note_rc
if [ "$latest" = "$base" ]; then
  title="Upstream Zoraxy ${tag} is available (fork has an earlier ${base} build)"
else
  title="Upstream Zoraxy ${tag} is available (fork is on ${base})"
fi

if [ "${DRY_RUN:-}" = "1" ]; then
  echo "DRY_RUN: would open issue: ${title}"
  exit 0
fi

if [ "$(gh api "repos/${GITHUB_REPOSITORY}" --jq .has_issues)" != "true" ]; then
  echo "::error::Upstream ${tag} is available, but Issues are turned off for ${GITHUB_REPOSITORY}, so no issue can be opened. Turn them on in Settings > General > Features."
  exit 1
fi

# Report each release once, even if its issue was closed
existing="$(gh issue list --repo "$GITHUB_REPOSITORY" --state all --label upstream-release --limit 100 --json title \
  --jq "[.[] | select(.title | startswith(\"Upstream Zoraxy ${tag} \"))] | length" 2>/dev/null || echo 0)"
if [ "$existing" != "0" ]; then
  echo "Already reported in an earlier issue." | tee -a "$SUMMARY"
  exit 0
fi

# Prepare the merge pull request
merge_note="The **Merge upstream release** workflow has been started for ${tag}; its pull request will appear under Pull requests in a few minutes."
if ! gh workflow run upstream-merge.yml --repo "$GITHUB_REPOSITORY" --ref main -f tag="$tag" 2>/tmp/dispatch.err; then
  merge_note="Could not start the **Merge upstream release** workflow automatically ($(head -c 300 /tmp/dispatch.err)). Run it from the Actions tab with tag \`${tag}\`."
fi

release_json="$(jq --arg t "$tag" '[.[] | select(.tag_name == $t)][0]' <<<"$releases_json")"
published="$(jq -r '.published_at // ""' <<<"$release_json" | cut -c1-10)"
notes="$(jq -r '.body // ""' <<<"$release_json" | head -c 50000)"
body_file="$(mktemp)"
cat >"$body_file" <<EOF
Upstream [${UPSTREAM_REPO}](https://github.com/${UPSTREAM_REPO}) published **${tag}** on ${published}. Waymark is based on **${base}**.

- Release: https://github.com/${UPSTREAM_REPO}/releases/tag/${tag}
- Upstream changes not yet in Waymark: https://github.com/${UPSTREAM_REPO}/compare/${since:-v${base}}...${tag}

${merge_note}

**To take the update**
1. Review the pull request "Merge upstream Zoraxy ${tag}". It merges the release into \`main\`, keeps the folders Waymark removed removed, notes the merge in CHANGELOG.md, and reports build and test results.
2. Merge it with **Create a merge commit** (not squash or rebase), so later upstream merges stay simple.
3. Pull \`main\` in GitHub Desktop, bump \`PRODUCT_VERSION\` in src/version_custom.go and the changelog, commit, then tag \`v<new version>\` to publish the build.

If the merge has conflicts, the workflow reports the files instead of opening a pull request.

<details><summary>Upstream release notes</summary>

${notes}

</details>
EOF

gh label create upstream-release --repo "$GITHUB_REPOSITORY" --color 0e8a16 \
  --description "New upstream Zoraxy release" --force >/dev/null
url="$(gh issue create --repo "$GITHUB_REPOSITORY" --title "$title" --label upstream-release --body-file "$body_file")"
echo "Opened ${url}" | tee -a "$SUMMARY"
