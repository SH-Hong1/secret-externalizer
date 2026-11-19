# OpenBao server for local development. It holds the original values that
# scans find, so it publishes no port on the host; only containers on the
# compose network reach it.
#
# The static seal and the self-initialisation below let one `compose up`
# produce an initialised, unsealed server that leaves no root token behind.
# The unseal key and the AppRole secret-ids are random files that the
# openbao-secrets container writes into the openbao-secrets volume; nothing
# secret is in this file. The unseal key lives in a volume next to the data,
# so this setup is for a single local machine only.

ui            = false
disable_mlock = true
api_addr      = "http://openbao:8200"
log_level     = "info"

storage "pebbledb" {
  path = "/openbao/file"
}

seal "static" {
  current_key_id = "local-1"
  current_key    = "file:///openbao/secrets/unseal/key"
}

# Traffic stays on the compose network, which is not reachable from outside
# the machine.
listener "tcp" {
  address     = "0.0.0.0:8200"
  tls_disable = true
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

  request "enable-approle" {
    operation = "update"
    path      = "sys/auth/approle"
    data = {
      type = "approle"
    }
  }
}

initialize "policies" {
  request "worker-policy" {
    operation = "update"
    path      = "sys/policies/acl/worker"
    data = {
      policy = <<-EOT
        path "transit/hmac/fingerprint" {
          capabilities = ["update"]
        }
        path "secret/data/pending/*" {
          capabilities = ["create", "update"]
        }
        path "secret/data/projects/*" {
          capabilities = ["create", "update"]
        }
        path "secret/metadata/pending/*" {
          capabilities = ["list", "delete"]
        }
      EOT
    }
  }

  request "web-policy" {
    operation = "update"
    path      = "sys/policies/acl/web"
    data = {
      policy = <<-EOT
        path "secret/data/pending/*" {
          capabilities = ["read"]
        }
        path "secret/data/projects/*" {
          capabilities = ["read"]
        }
      EOT
    }
  }
}

# Each role has a fixed role-id, which compose passes to the program as
# BAO_ROLE_ID, and one secret-id read from the openbao-secrets volume. The
# secret-id does not expire and has no use limit; tokens live for one hour and
# the program logs in again when one is refused.
initialize "roles" {
  request "worker-role" {
    operation = "update"
    path      = "auth/approle/role/worker"
    data = {
      token_policies     = ["worker"]
      token_ttl          = "1h"
      token_max_ttl      = "1h"
      secret_id_ttl      = 0
      secret_id_num_uses = 0
    }
  }

  request "worker-role-id" {
    operation = "update"
    path      = "auth/approle/role/worker/role-id"
    data = {
      role_id = "se-worker"
    }
  }

  request "worker-secret-id" {
    operation = "update"
    path      = "auth/approle/role/worker/custom-secret-id"
    data = {
      secret_id = {
        eval_type   = "string"
        eval_source = "file"
        path        = "/openbao/secrets/worker/secret-id"
      }
    }
  }

  request "web-role" {
    operation = "update"
    path      = "auth/approle/role/web"
    data = {
      token_policies     = ["web"]
      token_ttl          = "1h"
      token_max_ttl      = "1h"
      secret_id_ttl      = 0
      secret_id_num_uses = 0
    }
  }

  request "web-role-id" {
    operation = "update"
    path      = "auth/approle/role/web/role-id"
    data = {
      role_id = "se-web"
    }
  }

  request "web-secret-id" {
    operation = "update"
    path      = "auth/approle/role/web/custom-secret-id"
    data = {
      secret_id = {
        eval_type   = "string"
        eval_source = "file"
        path        = "/openbao/secrets/web/secret-id"
      }
    }
  }
}
