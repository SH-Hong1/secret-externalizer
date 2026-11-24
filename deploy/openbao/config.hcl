# OpenBao server for local development. It holds the original values that
# scans find and the externalized values, so it publishes no port on the
# host; only web, the worker, the applier, and openbao-roles reach it, on the
# internal compose network "bao".
#
# The seal is in a file of its own, seal-static.hcl, which the server reads
# with this one. An environment that holds real values replaces that file
# with an auto-unseal seal backed by a key service, and keeps this file.
#
# The seal and the self-initialisation below let one `compose up` produce an
# initialised, unsealed server that leaves no root token behind. The AppRole
# secret-ids and the TLS certificate are files that the openbao-secrets
# container writes into the openbao-secrets volume, and the admin role's
# secret-id is a file on the host; nothing secret is in this file.

ui            = false
disable_mlock = true
api_addr      = "https://openbao:8200"
log_level     = "info"

storage "pebbledb" {
  path = "/openbao/file"
}

# Only TLS is answered. The certificate is for the name openbao and for
# 127.0.0.1, and is signed by a CA whose certificate web, the worker, the
# applier, and openbao-roles trust; see deploy/openbao/make-secrets.sh.
listener "tcp" {
  address         = "0.0.0.0:8200"
  tls_cert_file   = "/openbao/secrets/tls/tls.crt"
  tls_key_file    = "/openbao/secrets/tls/tls.key"
  tls_min_version = "tls12"
}

# Every request and response is written to standard output, where
# `docker compose logs openbao` shows it. Audit entries hash secret values with
# HMAC, so original values do not appear there. OpenBao refuses to create an
# audit device through the API, so it is declared here rather than in a
# self-initialisation request.
audit "file" "stdout" {
  description = "Write audit entries to standard output."
  options {
    file_path = "stdout"
  }
}

# Self-initialisation runs once, on the first start against empty storage. The
# requests run in order with the root token, which is revoked afterwards.
initialize "engines" {
  # The KV mount comes first and its configuration last. A new KV v2 mount
  # refuses requests while it upgrades its storage, and a refused request
  # stops the server before it has finished initialising.
  request "mount-kv" {
    operation = "update"
    path      = "sys/mounts/secret"
    data = {
      type = "kv"
      options = {
        version = "2"
      }
    }
  }

  request "mount-transit" {
    operation = "update"
    path      = "sys/mounts/transit"
    data = {
      type = "transit"
    }
  }

  # The fingerprint key is never rotated: every fingerprint in postgres is an
  # HMAC under version 1, and a new version would make the same value produce
  # a different fingerprint.
  request "create-fingerprint-key" {
    operation = "update"
    path      = "transit/keys/fingerprint"
    data = {
      type = "aes256-gcm96"
    }
  }

  request "enable-approle" {
    operation = "update"
    path      = "sys/auth/approle"
    data = {
      type = "approle"
    }
  }
}

# The admin role is the one role self-initialisation makes. The
# openbao-roles service logs in with it on every start and writes the roles
# web, the worker, and the applier use, with their policies from
# deploy/openbao/policies/; see deploy/openbao/roles.sh. Changing a policy
# therefore needs no new storage.
#
# The admin policy may write and remove every ACL policy and every AppRole
# role but its own, so its token can grant any permission. Its secret-id is
# a file on the host that deploy/openbao/admin-secret-id.sh makes, handed by
# compose to OpenBao and openbao-roles alone. Its tokens live five minutes,
# and openbao-roles revokes its token when it ends.
initialize "admin" {
  request "admin-policy" {
    operation = "update"
    path      = "sys/policies/acl/admin"
    data = {
      policy = <<-EOT
        path "sys/policies/acl" {
          capabilities = ["list"]
        }
        path "sys/policies/acl/*" {
          capabilities = ["create", "update", "delete"]
        }
        path "sys/policies/acl/admin" {
          capabilities = ["deny"]
        }
        path "auth/approle/role" {
          capabilities = ["list"]
        }
        path "auth/approle/role/*" {
          capabilities = ["create", "update", "delete", "list"]
        }
        path "auth/approle/role/admin" {
          capabilities = ["deny"]
        }
        path "auth/approle/role/admin/*" {
          capabilities = ["deny"]
        }
      EOT
    }
  }

  request "admin-role" {
    operation = "update"
    path      = "auth/approle/role/admin"
    data = {
      token_policies     = ["admin"]
      token_ttl          = "5m"
      token_max_ttl      = "5m"
      secret_id_ttl      = 0
      secret_id_num_uses = 0
    }
  }

  request "admin-role-id" {
    operation = "update"
    path      = "auth/approle/role/admin/role-id"
    data = {
      role_id = "se-admin"
    }
  }

  request "admin-secret-id" {
    operation = "update"
    path      = "auth/approle/role/admin/custom-secret-id"
    data = {
      secret_id = {
        eval_type   = "string"
        eval_source = "file"
        path        = "/run/secrets/openbao-admin-secret-id"
      }
    }
  }
}

initialize "kv" {
  # KV v2 keeps 10 versions of a path by default and drops the oldest on the
  # eleventh write. An externalized value may have no other copy, so every
  # path keeps 100 versions; the applier cannot delete any of them.
  request "kv-max-versions" {
    operation = "update"
    path      = "secret/config"
    data = {
      max_versions = 100
    }
  }
}
