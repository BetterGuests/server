\ir ../bootstrap/001_roles_and_schema.sql

ALTER ROLE betterguests_migrator PASSWORD 'betterguests_migrator_test';
ALTER ROLE betterguests_app PASSWORD 'betterguests_app_test';

CREATE EXTENSION IF NOT EXISTS btree_gist WITH SCHEMA app;
