-- Modify "messages" table
ALTER TABLE "messages" ADD COLUMN "superseded_at" timestamptz NULL;
