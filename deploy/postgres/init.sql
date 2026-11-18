-- Runs once, when the postgres volume is first created.
-- The password below is for local development only.

CREATE ROLE keycloak WITH LOGIN PASSWORD 'dev-only-keycloak-db-password';
CREATE DATABASE keycloak OWNER keycloak;
