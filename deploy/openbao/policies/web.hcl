# The web role: reads an original only when an admin asks to reveal one,
# and reads externalized values. It writes nothing.
path "secret/data/pending/*" {
  capabilities = ["read"]
}
path "secret/data/projects/*" {
  capabilities = ["read"]
}
