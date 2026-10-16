#!/usr/bin/env bash
# Custom fork: report a new upstream Zoraxy release.
#
# Compares the latest upstream release (pre-releases excluded) with the
# upstream version this branch is based on (SYSTEM_VERSION in src/def.go).
# If upstream is newer and not reported before, it opens an issue labelled
# "upstream-release" and starts the "Merge upstream release" workflow, which
# prepares a pull request.
#
# Environment: GH_TOKEN, GITHUB_REPOSITORY, GITHUB_STEP_SUMMARY (set by
# Actions), UPSTREAM_REPO (default tobychui/zoraxy), DRY_RUN=1 to only print.
set -euo pipefail

UPSTREAM_REPO="${UPSTREAM_REPO:-tobychui/zoraxy}"
SUMMARY="${GITHUB_STEP_SUMMARY:-/dev/stdout}"

base="$(sed -n -E 's/^[[:space:]]*SYSTEM_VERSION[[:space:]]*=[[:space:]]*"([0-9]+\.[0-9]+\.[0-9]+)".*/\1/p' src/def.go | head -n1)"
if [ -z "$base" ]; then
  echo "::error::Could not read SYSTEM_VERSION from src/def.go"
  exit 1
fi

release_json="$(gh api "repos/${UPSTREAM_REPO}/releases/latest")"
tag="$(jq -r .tag_name <<<"$release_json")"
latest="${tag#v}"
if ! [[ "$latest" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "::warning::Latest upstream release tag '${tag}' is not a plain x.y.z version; skipping"
  exit 0
fi

newest="$(printf '%s\n%s\n' "$base" "$latest" | sort -V | tail -n1)"
if [ "$latest" = "$base" ] || [ "$newest" = "$base" ]; then
  echo "Up to date: this fork is based on upstream ${base}; latest upstream release is ${tag}." | tee -a "$SUMMARY"
  exit 0
fi

echo "Upstream ${tag} is available (this fork is based on ${base})." | tee -a "$SUMMARY"
title="Upstream Zoraxy ${tag} is available (fork is on ${base})"

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
if ! gh workflow run upstream-merge.yml --repo "$GITHUB_REPOSITORY" --ref custom -f tag="$tag" 2>/tmp/dispatch.err; then
  merge_note="Could not start the **Merge upstream release** workflow automatically ($(head -c 300 /tmp/dispatch.err)). Run it from the Actions tab with tag \`${tag}\`."
fi

published="$(jq -r '.published_at // ""' <<<"$release_json" | cut -c1-10)"
notes="$(jq -r '.body // ""' <<<"$release_json" | head -c 50000)"
body_file="$(mktemp)"
cat >"$body_file" <<EOF
Upstream [${UPSTREAM_REPO}](https://github.com/${UPSTREAM_REPO}) published **${tag}** on ${published}. This fork is based on **${base}**.

- Release: https://github.com/${UPSTREAM_REPO}/releases/tag/${tag}
- Changes since ${base}: https://github.com/${UPSTREAM_REPO}/compare/v${base}...${tag}

${merge_note}

**To take the update**
1. Review the pull request "Merge upstream Zoraxy ${tag}". It merges the release into \`custom\`, keeps the fork's removed folders removed, resets the version to \`${latest}-custom.1\`, and reports build and test results.
2. Merge it with **Create a merge commit** (not squash or rebase), so later upstream merges stay simple.
3. Fetch/pull \`custom\` in GitHub Desktop, then tag \`v${latest}-custom.1\` to publish the build.

If the merge has conflicts, the workflow reports the files instead of opening a pull request.

<details><summary>Upstream release notes</summary>

${notes}

</details>
EOF

gh label create upstream-release --repo "$GITHUB_REPOSITORY" --color 0e8a16 \
  --description "New upstream Zoraxy release" --force >/dev/null
url="$(gh issue create --repo "$GITHUB_REPOSITORY" --title "$title" --label upstream-release --body-file "$body_file")"
echo "Opened ${url}" | tee -a "$SUMMARY"
