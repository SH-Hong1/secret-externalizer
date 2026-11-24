#!/bin/sh
# Brings OpenBao's AppRole roles and ACL policies to what this script and
# deploy/openbao/policies/ declare. The openbao-roles compose service runs
# it on every `docker compose up`, after OpenBao is unsealed, so a changed
# policy file reaches a running server without removing any volume.
#
# Each role in $roles gets:
#   - the policy of the same name, from policies/<role>.hcl;
#   - token_policies of that policy alone, tokens that live for one hour,
#     and secret-ids that neither expire nor run out;
#   - the fixed role-id se-<role>, which compose passes as BAO_ROLE_ID;
#   - exactly one secret-id, the one in <role>/secret-id of the
#     openbao-secrets volume, which make-secrets.sh writes. It is registered
#     when OpenBao does not know it, and every other secret-id of the role is
#     destroyed.
# Then every role and policy that is neither declared here nor admin,
# default, or root is removed.
#
# The script logs in as the admin role, which self-initialisation in
# config.hcl creates with the secret-id from the host file that
# admin-secret-id.sh makes. It revokes its token when it ends. Secret-ids
# reach bao only on standard input, never as an argument, and the responses
# that hold them are read only by sed.
#
# Paths can be set for the tests in roles_test.sh:
#   OPENBAO_POLICY_DIR            /policies
#   OPENBAO_ROLE_SECRETS_DIR      /secrets  (holds <role>/secret-id)
#   OPENBAO_ADMIN_SECRET_ID_FILE  /run/secrets/openbao-admin-secret-id
# BAO_ADDR and BAO_CACERT must be set; bao reads them.
set -eu

roles="worker applier web"
kept_roles="admin"
kept_policies="admin default root"

policy_dir=${OPENBAO_POLICY_DIR:-/policies}
secrets_dir=${OPENBAO_ROLE_SECRETS_DIR:-/secrets}
admin_file=${OPENBAO_ADMIN_SECRET_ID_FILE:-/run/secrets/openbao-admin-secret-id}
: "${BAO_ADDR:?BAO_ADDR is not set}"
: "${BAO_CACERT:?BAO_CACERT is not set}"

# Every file is checked before the first request, so a missing one changes
# nothing on the server.
for f in "$admin_file" $(for r in $roles; do echo "$policy_dir/$r.hcl" "$secrets_dir/$r/secret-id"; done); do
	if [ ! -r "$f" ] || [ ! -s "$f" ]; then
		echo "$f is missing, empty, or unreadable" >&2
		exit 1
	fi
done

# json_items: prints the strings of a JSON list that bao printed with
# -format=json, one per line.
json_items() { sed -n 's/^ *"\([^"]*\)",\{0,1\}$/\1/p'; }

# json_accessor: prints the secret_id_accessor of a JSON response.
json_accessor() { sed -n 's/.*"secret_id_accessor": *"\([^"]*\)".*/\1/p'; }

# listed WORD LIST: succeeds when WORD is one of the words in LIST.
listed() {
	case " $2 " in
	*" $1 "*) return 0 ;;
	esac
	return 1
}

if ! BAO_TOKEN=$(bao write -field=token auth/approle/login role_id=se-admin secret_id=- <"$admin_file"); then
	echo "logging in to OpenBao as se-admin failed. Storage that self-initialisation prepared without the admin role cannot be changed; see docs/local-run.md" >&2
	exit 1
fi
export BAO_TOKEN
trap 'bao token revoke -self >/dev/null 2>&1 || echo "revoking the admin token failed; it expires within five minutes" >&2' EXIT

for r in $roles; do
	bao policy write "$r" "$policy_dir/$r.hcl" >/dev/null
	bao write "auth/approle/role/$r" token_policies="$r" token_ttl=1h token_max_ttl=1h \
		secret_id_ttl=0 secret_id_num_uses=0 >/dev/null
	bao write "auth/approle/role/$r/role-id" role_id="se-$r" >/dev/null

	current=$(bao write -format=json "auth/approle/role/$r/secret-id/lookup" secret_id=- <"$secrets_dir/$r/secret-id" | json_accessor)
	if [ -z "$current" ]; then
		current=$(bao write -format=json "auth/approle/role/$r/custom-secret-id" secret_id=- <"$secrets_dir/$r/secret-id" | json_accessor)
		if [ -z "$current" ]; then
			echo "registering the secret-id of $r returned no accessor" >&2
			exit 1
		fi
		echo "registered the secret-id of $r"
	fi
	for a in $(bao list -format=json "auth/approle/role/$r/secret-id" | json_items); do
		if [ "$a" != "$current" ]; then
			bao write "auth/approle/role/$r/secret-id-accessor/destroy" secret_id_accessor="$a" >/dev/null
			echo "destroyed another secret-id of $r"
		fi
	done
done

for r in $(bao list -format=json auth/approle/role | json_items); do
	if ! listed "$r" "$roles $kept_roles"; then
		bao delete "auth/approle/role/$r" >/dev/null
		echo "removed the role $r"
	fi
done
for p in $(bao list -format=json sys/policies/acl | json_items); do
	if ! listed "$p" "$roles $kept_policies"; then
		bao policy delete "$p" >/dev/null
		echo "removed the policy $p"
	fi
done
echo "roles and policies are up to date: $roles"
