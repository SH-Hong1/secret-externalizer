#!/bin/sh
# Checks a commit message against the format in AGENTS.md, "Commit messages".
#
#   commit-lint.sh <file>           checks the commit message in <file>
#   commit-lint.sh --title <text>   checks a pull request title
#   commit-lint.sh --closes <file>  checks that a pull request body in <file>
#                                   closes an issue
#
# It exits 0 when the message is accepted and 1 with the reason on standard
# error when it is not.
set -eu

# Character ranges such as [A-Z] mean the ASCII letters only in the C locale.
LC_ALL=C
export LC_ALL

types='build|ci|docs|feat|fix|perf|refactor|revert|style|test'
max=72

fail() {
	printf 'commit message rejected: %s\n' "$1" >&2
	printf 'format: <type>(<scope>): <subject>, then a blank line, a body, and "Refs #<issue>"\n' >&2
	printf 'types: %s\n' "$(printf '%s' "$types" | tr '|' ' ')" >&2
	exit 1
}

check_header() {
	header=$1
	[ -n "$header" ] || fail 'the first line is empty'
	printf '%s\n' "$header" | grep -Eq "^($types)(\([a-z0-9][a-z0-9-]*\))?!?: [^ ]" ||
		fail "the first line is not \"<type>(<scope>): <subject>\": $header"
	[ "${#header}" -le "$max" ] || fail "the first line is longer than $max characters"
	subject=${header#*: }
	case $subject in
	[A-Z]*) fail 'the subject starts with an upper-case letter' ;;
	*.) fail 'the subject ends with a full stop' ;;
	esac
}

case ${1-} in
--title)
	[ $# -eq 2 ] || fail 'usage: commit-lint.sh --title <text>'
	check_header "$2"
	exit 0
	;;
--closes)
	[ $# -eq 2 ] || fail 'usage: commit-lint.sh --closes <file>'
	grep -Eiq '^(close[sd]?|fix(e[sd])?|resolve[sd]?) #[0-9]+' "$2" ||
		fail 'the pull request body has no line "Closes #<issue>"'
	exit 0
	;;
esac

[ $# -eq 1 ] || fail 'usage: commit-lint.sh <file>'

# Everything from the scissors line on, and every comment line, is not part
# of the message. git removes them after this hook runs.
message=$(sed -e '/^# -\{24\} >8 -\{24\}$/,$d' -e '/^#/d' "$1")
header=$(printf '%s\n' "$message" | sed -n '1p')

# Messages that git writes itself are left alone.
case $header in
'Merge '* | 'fixup! '* | 'squash! '* | 'amend! '*) exit 0 ;;
esac

check_header "$header"

second=$(printf '%s\n' "$message" | sed -n '2p')
[ -z "$second" ] || fail 'the second line is not blank'

printf '%s\n' "$message" | sed '1d' | grep -Eiq '^(refs|close[sd]?|fix(e[sd])?|resolve[sd]?) #[0-9]+' ||
	fail 'no line names an issue, for example "Refs #12"'
