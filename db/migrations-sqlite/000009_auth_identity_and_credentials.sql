-- +goose Up
-- create "app_user" table
CREATE TABLE "app_user" ("id" text NOT NULL, "username" text NOT NULL, "username_norm" text NOT NULL, "email" text NULL, "email_norm" text NULL, "email_verified_at" integer NULL, "display_name" text NOT NULL DEFAULT '', "timezone" text NULL, "locale" text NULL, "state" text NOT NULL DEFAULT 'active', "session_epoch" integer NOT NULL DEFAULT 0, "last_login_at" integer NULL, "failed_logins" integer NOT NULL DEFAULT 0, "locked_until_at" integer NULL, "mfa_totp_secret_enc" blob NULL, "mfa_enrolled_at" integer NULL, "mfa_required" integer NOT NULL DEFAULT 0, "deleted_at" integer NULL, "created_at" integer NOT NULL, "updated_at" integer NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "app_user_state_enum" CHECK (state IN ('pending', 'active', 'suspended', 'disabled')), CONSTRAINT "app_user_mfa_required_bool" CHECK (mfa_required IN (0, 1))) STRICT;
-- create index "ux_user_username" to table: "app_user"
CREATE UNIQUE INDEX "ux_user_username" ON "app_user" ("username_norm") WHERE deleted_at IS NULL;
-- create index "ux_user_email" to table: "app_user"
CREATE UNIQUE INDEX "ux_user_email" ON "app_user" ("email_norm") WHERE email_norm IS NOT NULL AND deleted_at IS NULL;
-- create "user_identity" table
CREATE TABLE "user_identity" ("id" text NOT NULL, "user_id" text NOT NULL, "provider" text NOT NULL, "provider_key" text NOT NULL DEFAULT '', "subject" text NOT NULL, "password_hash" text NULL, "password_algo" text NULL, "password_set_at" integer NULL, "must_reset" integer NOT NULL DEFAULT 0, "access_token_enc" blob NULL, "refresh_token_enc" blob NULL, "token_expires_at" integer NULL, "scopes" text NOT NULL DEFAULT '', "profile_json" text NOT NULL DEFAULT '{}', "last_used_at" integer NULL, "created_at" integer NOT NULL, "updated_at" integer NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "user_identity_user" FOREIGN KEY ("user_id") REFERENCES "app_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "user_identity_provider_enum" CHECK (provider IN ('local', 'discord', 'oidc')), CONSTRAINT "user_identity_password_algo_enum" CHECK (password_algo IS NULL OR password_algo IN ('argon2id')), CONSTRAINT "user_identity_must_reset_bool" CHECK (must_reset IN (0, 1))) STRICT;
-- create index "ux_identity_subject" to table: "user_identity"
CREATE UNIQUE INDEX "ux_identity_subject" ON "user_identity" ("provider", "provider_key", "subject");
-- create index "ix_identity_user" to table: "user_identity"
CREATE INDEX "ix_identity_user" ON "user_identity" ("user_id");
-- create "session" table
CREATE TABLE "session" ("id" text NOT NULL, "user_id" text NOT NULL, "token_hash" blob NOT NULL, "identity_id" text NULL, "session_epoch" integer NOT NULL DEFAULT 0, "created_at" integer NOT NULL, "last_seen_at" integer NOT NULL, "expires_at" integer NOT NULL, "absolute_expires_at" integer NOT NULL, "revoked_at" integer NULL, "ip" text NOT NULL DEFAULT '', "user_agent" text NOT NULL DEFAULT '', "mfa_satisfied_at" integer NULL, PRIMARY KEY ("id"), CONSTRAINT "session_user" FOREIGN KEY ("user_id") REFERENCES "app_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "session_identity" FOREIGN KEY ("identity_id") REFERENCES "user_identity" ("id") ON UPDATE NO ACTION ON DELETE SET NULL) STRICT;
-- create index "ux_session_token" to table: "session"
CREATE UNIQUE INDEX "ux_session_token" ON "session" ("token_hash");
-- create index "ix_session_user_active" to table: "session"
CREATE INDEX "ix_session_user_active" ON "session" ("user_id", "expires_at") WHERE revoked_at IS NULL;
-- create "service_account" table
CREATE TABLE "service_account" ("id" text NOT NULL, "name" text NOT NULL, "name_norm" text NOT NULL, "description" text NOT NULL DEFAULT '', "owner_user_id" text NOT NULL, "state" text NOT NULL DEFAULT 'active', "created_by" text NOT NULL, "created_at" integer NOT NULL, "updated_at" integer NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "service_account_owner" FOREIGN KEY ("owner_user_id") REFERENCES "app_user" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "service_account_creator" FOREIGN KEY ("created_by") REFERENCES "app_user" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "service_account_state_enum" CHECK (state IN ('active', 'disabled'))) STRICT;
-- create index "ux_service_account_name" to table: "service_account"
CREATE UNIQUE INDEX "ux_service_account_name" ON "service_account" ("name_norm");
-- create "api_token" table
CREATE TABLE "api_token" ("id" text NOT NULL, "prefix" text NOT NULL, "token_hash" blob NOT NULL, "service_account_id" text NOT NULL, "name" text NOT NULL, "scopes" text NOT NULL, "pepper_kid" text NOT NULL DEFAULT 'v1', "expires_at" integer NULL, "last_used_at" integer NULL, "last_used_ip" text NOT NULL DEFAULT '', "rate_limit_rpm" integer NOT NULL DEFAULT 600, "created_by" text NOT NULL, "revoked_at" integer NULL, "revoked_by" text NULL, "revoke_reason" text NOT NULL DEFAULT '', "created_at" integer NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "api_token_service_account" FOREIGN KEY ("service_account_id") REFERENCES "service_account" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "api_token_creator" FOREIGN KEY ("created_by") REFERENCES "app_user" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "api_token_revoker" FOREIGN KEY ("revoked_by") REFERENCES "app_user" ("id") ON UPDATE NO ACTION ON DELETE SET NULL) STRICT;
-- create index "ux_api_token_prefix" to table: "api_token"
CREATE UNIQUE INDEX "ux_api_token_prefix" ON "api_token" ("prefix");
-- create index "ux_api_token_hash" to table: "api_token"
CREATE UNIQUE INDEX "ux_api_token_hash" ON "api_token" ("token_hash");
-- create index "ix_api_token_sa" to table: "api_token"
CREATE INDEX "ix_api_token_sa" ON "api_token" ("service_account_id") WHERE revoked_at IS NULL;
-- create "feed_token" table
CREATE TABLE "feed_token" ("id" text NOT NULL, "token_hash" blob NOT NULL, "user_id" text NOT NULL, "kind" text NOT NULL, "pepper_kid" text NOT NULL DEFAULT 'v1', "revoked_at" integer NULL, "last_used_at" integer NULL, "created_at" integer NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "feed_token_user" FOREIGN KEY ("user_id") REFERENCES "app_user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "feed_token_kind_enum" CHECK (kind IN ('raids_ical', 'calendar_ical', 'standings_rss', 'articles_rss'))) STRICT;
-- create index "ux_feed_token_hash" to table: "feed_token"
CREATE UNIQUE INDEX "ux_feed_token_hash" ON "feed_token" ("token_hash");

-- +goose Down
-- Forward-only. This project ships no down migrations, ever: a down migration is code that runs
-- exactly once, in an emergency, on data that cannot be reproduced, written months earlier by
-- someone who never tested it against your database. Recovery is restoring the snapshot taken
-- immediately before this migration ran:
--
--     /data/backups/pre-<version>-<timestamp>.db.zst
--
-- The statement below aborts if goose is ever asked to run this block. Note that SQLite discards
-- RAISE()'s message outside a trigger body and reports "RAISE() may only be used within a
-- trigger-program" instead, so the path above — not the string below — is what an operator can
-- actually read.
SELECT RAISE(ABORT, 'DKP migrations are forward-only; restore /data/backups/pre-<ver>-*.db.zst');
