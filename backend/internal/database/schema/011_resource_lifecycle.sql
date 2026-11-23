ALTER TABLE agents ADD COLUMN emergency boolean NOT NULL DEFAULT false,ADD COLUMN removed boolean NOT NULL DEFAULT false;
ALTER TABLE model_resources ADD COLUMN emergency boolean NOT NULL DEFAULT false;
ALTER TABLE validations ADD COLUMN tokens integer, ADD COLUMN expected text, ADD COLUMN input_text text;
