# The applier role: apply checks and applies. It writes an externalized
# value under projects/ and reads it back to check the stored bytes before
# it changes a source file. It has no delete and no metadata access there,
# so it cannot remove a version.
#
# After an apply it removes the pending originals of the values it settled.
# It cannot store, list, or read an original.
path "transit/hmac/fingerprint" {
  capabilities = ["update"]
}
path "secret/data/projects/*" {
  capabilities = ["create", "update", "read"]
}
path "secret/metadata/pending/*" {
  capabilities = ["delete"]
}
