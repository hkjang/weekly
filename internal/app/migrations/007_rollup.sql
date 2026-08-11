INSERT INTO app_settings(key, value, secret) VALUES
 ('rollup.merge_similarity', '80', false),
 ('rollup.stall_weeks', '2', false),
 ('rollup.persistent_issue_weeks', '2', false),
 ('rollup.max_weeks', '80', false)
ON CONFLICT (key) DO NOTHING;
