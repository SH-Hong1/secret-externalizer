# The seal of the local OpenBao, read together with config.hcl.
#
# The static seal unseals with a 32-byte key read from a file. The file is
# on the host, outside every Docker volume, and compose hands it to the
# openbao container alone as the secret openbao-unseal-key; see
# deploy/openbao/unseal-key.sh. A copy of the openbao-data volume is
# therefore of no use without that file. Anyone who can read the file and
# the volume together can still read every value, so this seal is for a
# single local machine only.
seal "static" {
  current_key_id = "local-1"
  current_key    = "file:///run/secrets/openbao-unseal-key"
}
