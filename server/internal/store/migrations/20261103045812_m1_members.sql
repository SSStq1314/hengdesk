-- Modify "users" table
ALTER TABLE "users" ADD COLUMN "employee_no" character varying NOT NULL DEFAULT '';
-- Create index "user_org_id_employee_no" to table: "users"
CREATE UNIQUE INDEX "user_org_id_employee_no" ON "users" ("org_id", "employee_no") WHERE ((employee_no)::text <> ''::text);
