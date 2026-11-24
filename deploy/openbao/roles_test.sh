#!/bin/sh
# Tests for roles.sh. Run with no arguments from anywhere; prints "ok" and
# exits 0 when every case passes. OpenBao is not used: a fake bao on PATH
# keeps the policies, roles, role-ids, and secret-ids it is given as files
# under a state folder, and logs every argument it receives.
set -u

here="$(cd "$(dirname "$0")" && pwd)"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failures=0

fail() {
	echo "FAIL: $*" >&2
	failures=$((failures + 1))
}

state="$tmp/state"
mkdir -p "$tmp/path"
cat >"$tmp/path/bao" <<EOF
#!/bin/sh
# Understands the requests roles.sh makes, and nothing else.
state="$state"
printf '%s\n' "\$@" >>"\$state/argv.log"
[ -n "\${BAO_ADDR:-}" ] && [ -n "\${BAO_CACERT:-}" ] || { echo "fake bao: BAO_ADDR or BAO_CACERT unset" >&2; exit 2; }
if [ "\$*" = "write -field=token auth/approle/login role_id=se-admin secret_id=-" ]; then
	if [ "\$(cat)" = "\$(cat "\$state/admin-secret")" ]; then
		echo fake-admin-token
		exit 0
	fi
	echo "fake bao: invalid role or secret ID" >&2
	exit 2
fi
[ "\${BAO_TOKEN:-}" = fake-admin-token ] || { echo "fake bao: permission denied" >&2; exit 2; }
json_list() {
	printf '[\n'
	first=1
	for n in "\$@"; do
		[ \$first -eq 1 ] || printf ',\n'
		printf '  "%s"' "\$n"
		first=0
	done
	printf '\n]\n'
}
case "\$*" in
"policy write "*)
	cp "\$4" "\$state/policies/\$3"
	;;
"policy delete "*)
	rm "\$state/policies/\$3"
	;;
"list -format=json sys/policies/acl")
	json_list \$(ls "\$state/policies")
	;;
"list -format=json auth/approle/role")
	json_list \$(ls "\$state/roles")
	;;
"list -format=json auth/approle/role/"*"/secret-id")
	role=\${3#auth/approle/role/}
	role=\${role%/secret-id}
	set -- \$(ls "\$state/sids/\$role" 2>/dev/null)
	[ \$# -gt 0 ] || { echo "No value found" >&2; exit 2; }
	json_list "\$@"
	;;
"write -format=json auth/approle/role/"*"/secret-id/lookup secret_id=-")
	role=\${3#auth/approle/role/}
	role=\${role%/secret-id/lookup}
	secret=\$(cat)
	for a in \$(ls "\$state/sids/\$role" 2>/dev/null); do
		if [ "\$(cat "\$state/sids/\$role/\$a")" = "\$secret" ]; then
			printf '{\n  "data": {\n    "secret_id_accessor": "%s",\n    "secret_id_num_uses": 0\n  }\n}\n' "\$a"
		fi
	done
	;;
"write -format=json auth/approle/role/"*"/custom-secret-id secret_id=-")
	role=\${3#auth/approle/role/}
	role=\${role%/custom-secret-id}
	secret=\$(cat)
	n=\$(cat "\$state/next" 2>/dev/null || echo 1)
	echo \$((n + 1)) >"\$state/next"
	mkdir -p "\$state/sids/\$role"
	printf '%s' "\$secret" >"\$state/sids/\$role/accessor-\$n"
	printf '{\n  "data": {\n    "secret_id": "%s",\n    "secret_id_accessor": "accessor-%s"\n  }\n}\n' "\$secret" "\$n"
	;;
"write auth/approle/role/"*"/secret-id-accessor/destroy secret_id_accessor="*)
	role=\${2#auth/approle/role/}
	role=\${role%/secret-id-accessor/destroy}
	rm "\$state/sids/\$role/\${3#secret_id_accessor=}"
	;;
"write auth/approle/role/"*"/role-id role_id="*)
	role=\${2#auth/approle/role/}
	role=\${role%/role-id}
	echo "\${3#role_id=}" >"\$state/roleids/\$role"
	;;
"write auth/approle/role/"*)
	role=\${2#auth/approle/role/}
	shift 2
	echo "\$*" >"\$state/roles/\$role"
	;;
"delete auth/approle/role/"*)
	role=\${2#auth/approle/role/}
	rm -f "\$state/roles/\$role" "\$state/roleids/\$role"
	rm -rf "\$state/sids/\$role"
	;;
"token revoke -self")
	touch "\$state/revoked"
	;;
*)
	echo "fake bao: unexpected request: \$*" >&2
	exit 2
	;;
esac
EOF
chmod 755 "$tmp/path/bao"

secrets="$tmp/secrets"
admin_file="$tmp/admin-secret-id"
admin_secret=dev-only-fake-admin-secret-id-0000000000000000000000000000000000
worker_secret=dev-only-fake-worker-secret-id-000000000000000000000000000000000
applier_secret=dev-only-fake-applier-secret-id-00000000000000000000000000000000
web_secret=dev-only-fake-web-secret-id-0000000000000000000000000000000000000

# reset: a server that self-initialisation has just prepared, with only the
# admin role and the admin, default, and root policies, and the three
# secret-id files of make-secrets.sh.
reset() {
	rm -rf "$state" "$secrets"
	mkdir -p "$state/policies" "$state/roles" "$state/roleids" "$state/sids/admin"
	: >"$state/argv.log"
	printf '%s' "$admin_secret" >"$state/admin-secret"
	printf '%s' "$admin_secret" >"$state/sids/admin/accessor-admin"
	echo "token_policies=admin" >"$state/roles/admin"
	echo se-admin >"$state/roleids/admin"
	for p in admin default root; do
		echo "fake $p policy" >"$state/policies/$p"
	done
	printf '%s' "$admin_secret" >"$admin_file"
	for r in worker applier web; do
		mkdir -p "$secrets/$r"
	done
	printf '%s' "$worker_secret" >"$secrets/worker/secret-id"
	printf '%s' "$applier_secret" >"$secrets/applier/secret-id"
	printf '%s' "$web_secret" >"$secrets/web/secret-id"
}

# run_script: runs the script and sets status and output.
run_script() {
	output=$(env PATH="$tmp/path:$PATH" BAO_ADDR=https://openbao:8200 BAO_CACERT=/fake/ca.crt \
		OPENBAO_POLICY_DIR="$here/policies" OPENBAO_ROLE_SECRETS_DIR="$secrets" \
		OPENBAO_ADMIN_SECRET_ID_FILE="$admin_file" sh "$here/roles.sh" 2>&1)
	status=$?
}

# check_no_secret_shown: no secret-id is in the output or among the
# arguments bao received.
check_no_secret_shown() {
	for s in "$admin_secret" "$worker_secret" "$applier_secret" "$web_secret"; do
		case $output in
		*"$s"*) fail "$1: the output shows a secret-id" ;;
		esac
		if grep -q "$s" "$state/argv.log"; then
			fail "$1: a secret-id was passed to bao as an argument"
		fi
	done
}

# check_roles: each role has its policy from the policy file, the role-id
# se-<role>, and exactly one secret-id, the one in its file.
check_roles() {
	label=$1
	for r in worker applier web; do
		cmp -s "$here/policies/$r.hcl" "$state/policies/$r" || fail "$label: policy $r is not policies/$r.hcl"
		[ -e "$state/roles/$r" ] || {
			fail "$label: role $r is missing"
			continue
		}
		grep -q "token_policies=$r " "$state/roles/$r" || fail "$label: role $r does not carry the policy $r: $(cat "$state/roles/$r")"
		[ "$(cat "$state/roleids/$r" 2>/dev/null)" = "se-$r" ] || fail "$label: role $r has role-id '$(cat "$state/roleids/$r" 2>/dev/null)', want se-$r"
		set -- "$state/sids/$r"/*
		if [ $# -ne 1 ] || ! cmp -s "$1" "$secrets/$r/secret-id"; then
			fail "$label: role $r does not hold exactly its one secret-id"
		fi
	done
}

# A server that self-initialisation has just prepared.
reset
run_script
[ "$status" -eq 0 ] || fail "first run: exit $status: $output"
check_roles "first run"
check_no_secret_shown "first run"
[ -e "$state/revoked" ] || fail "first run: the admin token was not revoked"
for r in worker applier web; do
	case $output in
	*"registered the secret-id of $r"*) ;;
	*) fail "first run: the output does not say the secret-id of $r was registered: $output" ;;
	esac
done

# Running again changes no secret-id.
before=$(ls -R "$state/sids")
rm -f "$state/revoked"
run_script
[ "$status" -eq 0 ] || fail "second run: exit $status: $output"
[ "$(ls -R "$state/sids")" = "$before" ] || fail "second run: the secret-ids changed"
case $output in
*registered*) fail "second run: a secret-id was registered again: $output" ;;
esac
check_roles "second run"
[ -e "$state/revoked" ] || fail "second run: the admin token was not revoked"

# A role, a policy, and a secret-id that nothing declares are removed; the
# admin role and the admin, default, and root policies stay.
printf '%s' dev-only-fake-old-secret-id >"$state/sids/worker/accessor-old"
echo "token_policies=old" >"$state/roles/old"
echo "fake old policy" >"$state/policies/old"
run_script
[ "$status" -eq 0 ] || fail "stale entries: exit $status: $output"
check_roles "stale entries"
[ ! -e "$state/roles/old" ] || fail "stale entries: role old was kept"
[ ! -e "$state/policies/old" ] || fail "stale entries: policy old was kept"
[ -e "$state/roles/admin" ] || fail "stale entries: role admin was removed"
[ -e "$state/sids/admin/accessor-admin" ] || fail "stale entries: the admin secret-id was removed"
for p in admin default root; do
	[ -e "$state/policies/$p" ] || fail "stale entries: policy $p was removed"
done
grep -qx "policy delete default" "$state/argv.log" && fail "stale entries: asked to delete the default policy"
grep -qx "policy delete root" "$state/argv.log" && fail "stale entries: asked to delete the root policy"

# A changed secret-id file replaces the role's secret-id.
reset
run_script
worker_secret=dev-only-fake-worker-secret-id-111111111111111111111111111111111
printf '%s' "$worker_secret" >"$secrets/worker/secret-id"
run_script
[ "$status" -eq 0 ] || fail "new secret-id: exit $status: $output"
check_roles "new secret-id"
check_no_secret_shown "new secret-id"
worker_secret=dev-only-fake-worker-secret-id-000000000000000000000000000000000

# A wrong admin secret-id stops the script before it changes anything.
reset
printf '%s' dev-only-fake-wrong-admin-secret-id >"$admin_file"
run_script
[ "$status" -ne 0 ] || fail "wrong admin secret-id: exit 0"
case $output in
*se-admin*) ;;
*) fail "wrong admin secret-id: the output does not name se-admin: $output" ;;
esac
[ -z "$(ls "$state/roleids" | grep -v admin)" ] || fail "wrong admin secret-id: a role was written"

# A missing secret-id file or policy file stops the script before it logs
# in.
for missing in "$secrets/applier/secret-id" "$tmp/policies/web.hcl"; do
	reset
	rm -rf "$tmp/policies"
	cp -R "$here/policies" "$tmp/policies"
	rm "$missing"
	output=$(env PATH="$tmp/path:$PATH" BAO_ADDR=https://openbao:8200 BAO_CACERT=/fake/ca.crt \
		OPENBAO_POLICY_DIR="$tmp/policies" OPENBAO_ROLE_SECRETS_DIR="$secrets" \
		OPENBAO_ADMIN_SECRET_ID_FILE="$admin_file" sh "$here/roles.sh" 2>&1)
	status=$?
	[ "$status" -ne 0 ] || fail "missing $missing: exit 0"
	[ ! -s "$state/argv.log" ] || fail "missing $missing: bao was called: $(cat "$state/argv.log")"
done

if [ "$failures" -ne 0 ]; then
	exit 1
fi
echo ok
