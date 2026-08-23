-- Where a report item came from.
--
-- Provenance existed, in two shapes, and both were thrown away at the moment it
-- became useful. A Confluence candidate carried the pages behind it in
-- candidate_sources; a PPTX import carried slide numbers in the parse result.
-- Accepting the candidate or confirming the import wrote a report_items row and
-- kept neither. Six months later the item says what happened and nothing says
-- where it came from — and report_items is referenced by exactly one table, the
-- embeddings, so there was nowhere the answer could have been hiding.
--
-- One table for every source. The roadmap asks for Manual, Confluence, Jira,
-- Git, CI, ITSM and PPTX under one provenance model, and a per-connector table
-- would make "얼마나 많은 근거가 있는가" a different query for each.

CREATE TABLE IF NOT EXISTS report_item_sources (
  id             BIGSERIAL PRIMARY KEY,
  report_item_id BIGINT NOT NULL REFERENCES report_items(id) ON DELETE CASCADE,
  -- What kind of thing this is. Constrained rather than free text: a screen
  -- groups by it and counts it, and a typo would silently become a new source
  -- kind nobody chose.
  kind VARCHAR(20) NOT NULL,
  -- The identifier in the origin system: a page id, a slide list, a commit sha,
  -- an issue key. Kept as text because every system spells its ids differently
  -- and this table is not the place to normalise them.
  reference VARCHAR(240) NOT NULL DEFAULT '',
  title     VARCHAR(240) NOT NULL DEFAULT '',
  url       TEXT NOT NULL DEFAULT '',
  -- Anything a reader needs that the title does not carry: which slides, which
  -- page version, which activity.
  detail    TEXT NOT NULL DEFAULT '',
  -- When the evidence happened in its own system, which is not when it was
  -- recorded here. A commit from three weeks ago attached to this week's report
  -- is a fact worth being able to see.
  occurred_at TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT report_item_sources_kind_check CHECK (kind IN
    ('MANUAL','CONFLUENCE','PPTX','AI_TEXT','JIRA','GIT','CI','ITSM','API'))
);

-- Read whenever a report is opened, per item, in insertion order.
CREATE INDEX IF NOT EXISTS idx_report_item_sources_item
  ON report_item_sources(report_item_id, id);

-- "이 페이지가 어느 보고에 근거로 쓰였나" — the question that makes lineage
-- worth storing, asked from the origin system's end.
CREATE INDEX IF NOT EXISTS idx_report_item_sources_reference
  ON report_item_sources(kind, reference);
