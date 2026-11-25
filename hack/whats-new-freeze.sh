#!/usr/bin/env sh
# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT
#
# Freeze the docs' "What's new" page for a release just cut, and start the
# next one:
#
#   hack/whats-new-freeze.sh 0.15.0
#
# docs/whats-new/index.md is the current release's page; release-please
# stamped its front matter (version, and the title that is its heading)
# with the version when the release PR merged. This copies it to
# docs/whats-new/<version>.md (without the release-please markers, so later
# release PRs leave it alone, and with a link to the release), lists the
# copy in the zensical.toml nav above the older releases, and resets
# index.md to a placeholder (a maintenance-release page) for the next patch
# version. The first feature commit replaces the placeholder with its
# highlights, and the release PR corrects the version if the bump turns out
# bigger.
#
# A release made only of fixes and dependency updates therefore publishes
# the placeholder, stamped with its version, instead of relabelling the
# last release's highlights; and it gets no versioned page. When the page
# is still the untouched placeholder at release time, this changes nothing.
#
# The What's New workflow (.github/workflows/whats-new.yaml) runs this after
# every release and opens the result as a pull request. It is idempotent: a
# version already frozen is left alone. Run from the repository root.
set -eu

version=${1:?usage: whats-new-freeze.sh <version, e.g. 0.15.0>}
repo=${GITHUB_REPOSITORY:-bitwise-media-group/patchy}

if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "whats-new: $version is not a release version like 0.15.0" >&2
  exit 2
fi

page=docs/whats-new/index.md
frozen=docs/whats-new/$version.md
nav=zensical.toml

# placeholder is the body of a page no feature commit has written yet:
# what a release made only of fixes and dependency updates leaves behind.
placeholder() {
  cat <<EOF
<!--
  The current release's page. Its version and title (the page heading) live in the front matter, and release-please
  rewrites both marked lines when it opens a release PR (release-please-config.json, extra-files). Keep every other
  version string, image paths included, off those lines. After each release, the What's New workflow freezes this
  page into <version>.md and starts it again (hack/whats-new-freeze.sh; AGENTS.md, "What's new").
-->

This is a maintenance release: dependency updates and fixes, with no new features. The
[changelog](https://github.com/$repo/blob/main/CHANGELOG.md) lists every change.
EOF
}

if [ -f "$frozen" ]; then
  echo "whats-new: $frozen already exists; nothing to do"
  exit 0
fi

# The front matter's version names the page's release.
current=$(awk 'NR == 1 && $0 != "---" { exit } NR > 1 && $0 == "---" { exit }
  sub(/^version:[[:space:]]*/, "") { sub(/[[:space:]]*#.*$/, ""); gsub(/"/, ""); print; exit }' "$page")
if [ "$current" != "$version" ]; then
  echo "whats-new: $page is for ${current:-(no front matter version)}, not $version; was it already frozen or edited?" >&2
  exit 1
fi

# A release with nothing to highlight gets no page of its own: the
# untouched placeholder stays as it is (the next release PR stamps it with
# that release's version), so there is nothing to freeze or commit.
body=$(awk 'matter < 2 { if ($0 == "---") matter++; next } { print }' "$page" | sed '/./,$!d')
if [ "$body" = "$(placeholder)" ]; then
  echo "whats-new: $version had nothing to highlight; $page stays the placeholder"
  exit 0
fi

# The frozen copy: the front matter without its comments or release-please
# markers (its screenshots list stays, as a record), then a link to the
# release, then the body without the header comment
# (the comment block before any content). cat -s folds the blank lines
# the removals leave behind.
awk -v version="$version" -v repo="$repo" '
  NR == 1 && $0 == "---" { matter = 1; print; next }
  matter {
    # YAML comments describe the live page (what gets shot, what gets
    # stamped); the frozen copy keeps only the values.
    if ($0 ~ /^[[:space:]]*#/) next
    sub(/[[:space:]]*# x-release-please-version$/, "")
    print
    if ($0 == "---") {
      matter = 0
      printf "\nReleased as [v%s](https://github.com/%s/releases/tag/v%s).\n\n", version, repo, version
    }
    next
  }
  !content && $0 == "<!--" { header = 1 }
  header { if ($0 == "-->") header = 0; next }
  $0 != "" { content = 1 }
  { print }
' "$page" | cat -s >"$frozen"

# List it in the nav: first after the section index, so newest first.
awk -v version="$version" '
  { print }
  $0 ~ /^[[:space:]]*"whats-new\/index\.md",$/ {
    match($0, /^[[:space:]]*/)
    printf "%s{ \"%s\" = \"whats-new/%s.md\" },\n", substr($0, 1, RLENGTH), version, version
    listed = 1
  }
  END { if (!listed) exit 3 }
' "$nav" >"$nav.tmp" || {
  rm -f "$nav.tmp" "$frozen"
  echo "whats-new: no \"whats-new/index.md\" entry in the $nav nav to list $version under" >&2
  exit 1
}
mv "$nav.tmp" "$nav"

# The next page starts as the placeholder, with the front matter for the
# next patch version.
next=$(printf '%s\n' "$version" | awk -F. '{ printf "%d.%d.%d", $1, $2, $3 + 1 }')
{
  printf -- '---\nversion: %s # x-release-please-version\n' "$next"
  printf 'title: "What'"'"'s new in Patchy %s" # x-release-please-version\n---\n\n' "$next"
  placeholder
} >"$page"

echo "whats-new: froze $version into $frozen; $page is now for $next"
