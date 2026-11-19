-- Runs once, when the postgres volume is first created.
-- The password below is for local development only.

CREATE ROLE keycloak WITH LOGIN PASSWORD 'dev-only-keycloak-db-password';
CREATE DATABASE keycloak OWNER keycloak;

-- The role web and worker share. It owns the application database and the
-- test database, so the migrations can create tables in them and the
-- integration tests can create a schema per test.
CREATE ROLE se_app WITH LOGIN PASSWORD 'dev-only-se-app-password';
ALTER DATABASE secret_externalizer OWNER TO se_app;
CREATE DATABASE secret_externalizer_test OWNER se_app;
