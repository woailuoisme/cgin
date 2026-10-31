-- Modify "user" table
ALTER TABLE "user" DROP COLUMN "image", ADD COLUMN "avatar_path" text NULL;
-- Drop "media" table
DROP TABLE "media";
