-- Modify "users" table
ALTER TABLE "users" ADD COLUMN "oidc_issuer" character varying NOT NULL DEFAULT '', ADD COLUMN "oidc_subject" character varying NOT NULL DEFAULT '';
-- Create index "user_oidc_issuer_oidc_subject" to table: "users"
CREATE UNIQUE INDEX "user_oidc_issuer_oidc_subject" ON "users" ("oidc_issuer", "oidc_subject") WHERE ((oidc_subject)::text <> ''::text);
