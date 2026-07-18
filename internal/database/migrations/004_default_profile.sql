INSERT INTO agent_profiles(name, allowed_tools)
VALUES (
    'Default',
    '["add_issue_comment","create_pull_request","get_commit","get_file_contents","issue_read","label_write","list_branches","list_commits","pull_request_read","pull_request_review_write","request_pull_request_reviewers"]'::jsonb
)
ON CONFLICT(name) DO NOTHING;
