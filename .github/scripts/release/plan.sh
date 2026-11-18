#!/usr/bin/env bash
# plan.sh — decides what the release workflow does for the commit it runs on.
#
# Usage: plan.sh <repo-root> <tags-file> <releases-file>
#
#   <repo-root>      a checkout of the commit being released, with every tag
#                    present locally: a tag is judged by the notes in its own
#                    commit.
#   <tags-file>      the remote's tags, one per line: bare names, refs/tags/
#                    refs or `git ls-remote --tags` output.
#   <releases-file>  the tag names that have a GitHub Release, one per line.
#
# Output, one JSON object on stdout:
#
#   {"mode":"release","version":"v2.2.0"}   notes exist, the tag does not
#   {"mode":"finish","version":"v2.1.1"}    the latest tag has no GitHub Release
#   {"mode":"none","version":""}            nothing to do
#
# A release is the merge of its notes: docs/releases/vX.Y.Z.md with no tag
# vX.Y.Z. Fails, naming the versions, on more than one pending version and on
# a pending version that is not newer than the latest tag; notes added for an
# old version would otherwise publish a release older than the one before it.
# Only the latest tag is ever finished, so an old tag that never had a release
# is never rebuilt with today's build config.
set -euo pipefail

die() {
  echo "::error::$*" >&2
  exit 1
}

((${#} == 3)) || die "usage: plan.sh <repo-root> <tags-file> <releases-file>"
root="$1"
tags_file="$2"
releases_file="$3"
readonly semver='^v[0-9]+\.[0-9]+\.[0-9]+$'

git -C "$root" rev-parse --git-dir >/dev/null 2>&1 || die "$root is not a git repository"
[[ -r "$tags_file" ]] || die "cannot read the tag list $tags_file"
[[ -r "$releases_file" ]] || die "cannot read the release list $releases_file"

# Last whitespace-separated field, without refs/tags/ or the peeled ^{} suffix.
names() {
  awk 'NF { print $NF }' "$1" | sed -e 's|^refs/tags/||' -e 's|\^{}$||' | grep -E "$semver" | sort -uV || true
}

tags="$(names "$tags_file")"
releases="$(names "$releases_file")"
latest="$(tail -n 1 <<<"$tags")"

notes=""
if [[ -d "$root/docs/releases" ]]; then
  notes="$(find "$root/docs/releases" -maxdepth 1 -name 'v*.md' -exec basename {} .md \; | grep -E "$semver" | sort -V || true)"
fi

pending=""
while IFS= read -r v; do
  [[ -z "$v" ]] && continue
  grep -qxF "$v" <<<"$tags" || pending+="$v"$'\n'
done <<<"$notes"
pending="${pending%$'\n'}"

emit() { printf '{"mode":"%s","version":"%s"}\n' "$1" "$2"; }

if [[ -n "$pending" ]]; then
  if [[ "$(wc -l <<<"$pending")" -gt 1 ]]; then
    die "more than one release is pending: $(tr '\n' ' ' <<<"$pending")"
  fi
  if [[ -n "$latest" && "$(printf '%s\n%s\n' "$latest" "$pending" | sort -V | tail -n 1)" != "$pending" ]]; then
    die "$pending is not newer than the latest tag $latest; notes for an old version cannot be released"
  fi
  emit release "$pending"
  exit 0
fi

if [[ -n "$latest" ]] && ! grep -qxF "$latest" <<<"$releases" \
  && git -C "$root" cat-file -e "refs/tags/${latest}^{commit}:docs/releases/${latest}.md" 2>/dev/null; then
  emit finish "$latest"
  exit 0
fi
emit none ""
