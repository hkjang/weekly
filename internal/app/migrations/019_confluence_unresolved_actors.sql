-- Counting what the sync produced was never the whole story: a Confluence
-- account that matches no Weekly user drops every page it touched, and until
-- now nothing recorded that it happened.
ALTER TABLE confluence_sync_state ADD COLUMN IF NOT EXISTS actors_unresolved integer NOT NULL DEFAULT 0;
ALTER TABLE confluence_sync_state ADD COLUMN IF NOT EXISTS pages_unattributed integer NOT NULL DEFAULT 0;
