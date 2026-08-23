-- A template per organisation.
--
-- One deployment, one deck. That holds until two divisions have their own
-- report format, which in an organisation large enough to need this product is
-- most of them — and then everyone exports somebody else's cover page.
--
-- The row is keyed by organisation with NULL meaning the house default, so the
-- existing single template becomes the default rather than being migrated away.

ALTER TABLE pptx_templates DROP CONSTRAINT IF EXISTS pptx_templates_id_check;
ALTER TABLE pptx_templates ADD COLUMN IF NOT EXISTS organization_id BIGINT
  REFERENCES organizations(id) ON DELETE CASCADE;

-- id was a fixed 1. It becomes an ordinary identity so more than one row can
-- exist; the existing row keeps id 1 and organization_id NULL, which is exactly
-- the default template it already was.
CREATE SEQUENCE IF NOT EXISTS pptx_templates_id_seq OWNED BY pptx_templates.id;
SELECT setval('pptx_templates_id_seq', GREATEST((SELECT coalesce(max(id),1) FROM pptx_templates), 1));
ALTER TABLE pptx_templates ALTER COLUMN id SET DEFAULT nextval('pptx_templates_id_seq');
ALTER TABLE pptx_templates ALTER COLUMN id TYPE BIGINT;

-- One template per organisation, and one default. NULLs compare as distinct in
-- a plain unique index, so the default needs its own partial one or a second
-- house template could be inserted and silently shadow the first.
CREATE UNIQUE INDEX IF NOT EXISTS idx_pptx_templates_org
  ON pptx_templates(organization_id) WHERE organization_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_pptx_templates_default
  ON pptx_templates((organization_id IS NULL)) WHERE organization_id IS NULL;
