ALTER TABLE management_grants ADD COLUMN export_revision integer NOT NULL DEFAULT 1 CHECK(export_revision>0);
