-- Create "user" table
CREATE TABLE "user" (
  "id" text NOT NULL DEFAULT (uuidv7())::text,
  "name" text NOT NULL,
  "email" text NOT NULL,
  "email_verified" boolean NOT NULL DEFAULT false,
  "image" text NULL,
  "role" text NOT NULL DEFAULT 'user',
  "status" text NOT NULL DEFAULT 'active',
  "banned" boolean NOT NULL DEFAULT false,
  "two_factor_enabled" boolean NOT NULL DEFAULT false,
  "username" text NULL,
  "display_username" text NULL,
  "phone_number" text NULL,
  "phone_number_verified" boolean NOT NULL DEFAULT false,
  "is_anonymous" boolean NOT NULL DEFAULT false,
  "last_login_method" text NULL,
  "ban_reason" text NULL,
  "ban_expires" timestamptz NULL,
  "last_login_at" timestamptz NULL,
  "created_at" timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  "updated_at" timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  "deleted_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "ck_user_role" CHECK (role = ANY (ARRAY['user'::text, 'admin'::text])),
  CONSTRAINT "ck_user_status" CHECK (status = ANY (ARRAY['active'::text, 'inactive'::text, 'banned'::text]))
);
-- Create index "idx_user_deleted_at" to table: "user"
CREATE INDEX "idx_user_deleted_at" ON "user" ("deleted_at");
-- Create index "idx_user_email" to table: "user"
CREATE UNIQUE INDEX "idx_user_email" ON "user" ((lower(email))) WHERE (deleted_at IS NULL);
-- Create index "idx_user_phone_number" to table: "user"
CREATE UNIQUE INDEX "idx_user_phone_number" ON "user" ("phone_number") WHERE ((deleted_at IS NULL) AND (phone_number IS NOT NULL));
-- Create index "idx_user_role" to table: "user"
CREATE INDEX "idx_user_role" ON "user" ("role") WHERE (deleted_at IS NULL);
-- Create index "idx_user_status" to table: "user"
CREATE INDEX "idx_user_status" ON "user" ("status");
-- Create index "idx_user_username" to table: "user"
CREATE UNIQUE INDEX "idx_user_username" ON "user" ((lower(username))) WHERE ((deleted_at IS NULL) AND (username IS NOT NULL));
-- Create "verification" table
CREATE TABLE "verification" (
  "id" text NOT NULL DEFAULT (uuidv7())::text,
  "identifier" text NOT NULL,
  "value" text NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "created_at" timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  "updated_at" timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY ("id")
);
-- Create index "idx_verification_expires_at" to table: "verification"
CREATE INDEX "idx_verification_expires_at" ON "verification" ("expires_at");
-- Create index "idx_verification_identifier_value" to table: "verification"
CREATE INDEX "idx_verification_identifier_value" ON "verification" ("identifier", "value");
-- Create "account" table
CREATE TABLE "account" (
  "id" text NOT NULL DEFAULT (uuidv7())::text,
  "user_id" text NOT NULL,
  "account_id" text NOT NULL,
  "provider_id" text NOT NULL,
  "password" text NULL,
  "access_token" text NULL,
  "refresh_token" text NULL,
  "id_token" text NULL,
  "access_token_expires_at" timestamptz NULL,
  "refresh_token_expires_at" timestamptz NULL,
  "scope" text NULL,
  "created_at" timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  "updated_at" timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY ("id"),
  CONSTRAINT "account_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "user" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_account_provider_account" to table: "account"
CREATE UNIQUE INDEX "idx_account_provider_account" ON "account" ("provider_id", "account_id");
-- Create index "idx_account_user_id" to table: "account"
CREATE INDEX "idx_account_user_id" ON "account" ("user_id");
