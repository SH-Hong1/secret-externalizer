# The worker role: scans. The scanners run in the same container and can
# read its secret-id, so this policy holds only what a scan needs and no
# path under secret/data/projects/, where the externalized values are.
#
# A scan fingerprints the values it finds, keeps each original under
# pending/ for an admin to reveal, and removes the originals that no bundle
# needs any more. It cannot read an original back.
path "transit/hmac/fingerprint" {
  capabilities = ["update"]
}
path "secret/data/pending/*" {
  capabilities = ["create", "update"]
}
path "secret/metadata/pending/*" {
  capabilities = ["list", "delete"]
}
