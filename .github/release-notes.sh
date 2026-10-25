#!/usr/bin/env bash
# Writes the release notes of a tag to standard output, in Markdown: every commit since the
# previous release tag, grouped by its Conventional Commits type (feat, fix, docs, ...), so
# commits pushed straight to main appear as well as merged pull requests. (gh's --generate-notes
# lists only pull requests.) The release workflows run it; to preview the notes of a release
# before tagging it, run it locally with the tag to be:
#
#   .github/release-notes.sh v0.3.0                                            Lyth
#   .github/release-notes.sh vscode-v0.2.0 'vscode-v*' editors/vscode-lyth     the VS Code extension
#
# The second argument is the glob of the release tags to compare with (default: Lyth's v1.2.3
# tags); further arguments limit the notes to commits that touch those paths. A tag that does not
# exist yet runs the notes to HEAD. A commit marked breaking (type!: or a BREAKING CHANGE line in
# its body) is listed under "Breaking changes" instead of its type's section.
set -euo pipefail

tag=$1
pattern=${2:-'v[0-9]*.[0-9]*.[0-9]*'}
shift $(($# < 2 ? $# : 2))
cd "$(git rev-parse --show-toplevel)"

# The previous release is the newest matching tag before the tag, or at or before HEAD when the
# tag is yet to be made.
end=HEAD since=HEAD
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
	end=$tag since=$tag^
fi
prev=$(git describe --tags --abbrev=0 --match "$pattern" "$since" 2>/dev/null || true)
range=${prev:+$prev..}$end
repo=${GITHUB_REPOSITORY:-}
if [ -z "$repo" ]; then
	repo=$(git remote get-url origin 2>/dev/null || true)
	repo=$(printf '%s' "$repo" | sed -E 's#^(https://github\.com/|git@github\.com:)##; s#\.git$##')
fi

# One line per entry: the section's number, a tab, and the entry. Sections print in this order.
sections=("Breaking changes" "Features" "Fixes" "Performance" "Documentation" "Tests" "Build and releases" "Other changes")
entries=$(
	git log --no-merges --reverse --format='%h %s' "$range" -- "$@" | while read -r hash subject; do
		section=7 scope='' text=$subject breaking=''
		if [[ $subject =~ ^([a-z]+)(\(([^\)]*)\))?(!)?:\ (.*)$ ]]; then
			type=${BASH_REMATCH[1]} scope=${BASH_REMATCH[3]} breaking=${BASH_REMATCH[4]} text=${BASH_REMATCH[5]}
			case $type in
			(feat) section=1 ;;
			(fix) section=2 ;;
			(perf) section=3 ;;
			(docs) section=4 ;;
			(test) section=5 ;;
			(build | ci) section=6 ;;
			(chore)
				# the version bump that opens each development cycle says nothing
				[[ $text =~ ^start\ .*development$ ]] && continue
				;;
			esac
		fi
		if [ -z "$breaking" ] && git log -1 --format=%b "$hash" | grep -q '^BREAKING[ -]CHANGE'; then
			breaking=!
		fi
		entry="- ${scope:+**$scope:** }$text ($hash)"
		[ -n "$breaking" ] && section=0
		printf '%s\t%s\n' "$section" "$entry"
	done
)

if [ -z "$entries" ]; then
	echo "No changes${prev:+ since $prev}."
else
	current=''
	while IFS=$'\t' read -r section entry; do
		if [ "$section" != "$current" ]; then
			[ -n "$current" ] && echo
			echo "## ${sections[$section]}"
			echo
			current=$section
		fi
		echo "$entry"
	done < <(printf '%s\n' "$entries" | sort -s -n -k1,1)
fi

if [ -n "$prev" ] && [ -n "$repo" ]; then
	echo
	echo "**Full changelog:** https://github.com/$repo/compare/$prev...$tag"
fi
