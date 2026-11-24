#!/bin/sh
# Turns the rules reviewer's verdict into the result of the CI review job.
#
#   review-verdict.sh <verdict-file> <override>
#
# <verdict-file> holds the reviewer's structured output, {"violations": [...]},
# where each violation has file, line, section, and fix. <override> is "true"
# when the pull request carries the label "review: override".
#
# It prints the pull request comment on standard output, or nothing when there
# is nothing to say, and exits
#   0 when there are no violations, or when <override> is true and none of
#     them is in the Security section;
#   1 when a violation blocks the merge;
#   2 when the file holds no readable verdict. The override label does not
#     apply then: an unreviewed change must not pass.
set -eu

[ $# -eq 2 ] || {
	echo 'usage: review-verdict.sh <verdict-file> <true|false>' >&2
	exit 2
}
file=$1
override=$2

if ! listed=$(jq -r -s '
	if length != 1 then error("no verdict") else .[0].violations end
	| if type != "array" then error("no verdict") else . end
	| .[:10][]
	| "- \(.file):\(.line) — \(.section) — \(.fix)"
' "$file" 2>/dev/null); then
	echo '## AI 규칙 검토'
	echo
	echo '검토자가 판정을 내지 못했습니다. 작업을 다시 실행하세요.'
	exit 2
fi

[ -n "$listed" ] || exit 0

security=$(jq '[.violations[:10][] | select(.section == "Security")] | length' "$file")

echo '## AI 규칙 검토'
echo
echo 'AGENTS.md의 규칙과 어긋나는 곳입니다.'
echo
printf '%s\n' "$listed"
echo
if [ "$override" = true ] && [ "$security" -eq 0 ]; then
	echo '`review: override` 라벨이 있어 통과시킵니다.'
	exit 0
fi
if [ "$override" = true ]; then
	echo 'Security 절 위반은 `review: override` 라벨로 통과시킬 수 없습니다.'
else
	echo '고칠 수 없는 지적이면 `review: override` 라벨을 붙여 통과시킬 수 있습니다. Security 절 위반은 예외입니다.'
fi
exit 1
