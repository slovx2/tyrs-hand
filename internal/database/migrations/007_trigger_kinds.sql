ALTER TABLE trigger_rules
    ADD COLUMN trigger_kind text,
    ADD COLUMN trigger_value text;

UPDATE trigger_rules AS rule
SET name = 'command',
    trigger_kind = 'slash_command',
    trigger_value = 'tyrs-hand',
    mention_required = false,
    version = version + 1,
    updated_at = now()
WHERE rule.name = 'mention'
  AND rule.event_name = 'issue_comment'
  AND rule.action = 'created'
  AND rule.mention_required = true
  AND NOT EXISTS (
      SELECT 1 FROM trigger_rules AS existing
      WHERE existing.repository_id = rule.repository_id
        AND existing.name = 'command'
  );

UPDATE trigger_rules
SET trigger_kind = CASE WHEN mention_required THEN 'legacy_mention' ELSE 'event' END
WHERE trigger_kind IS NULL;

ALTER TABLE trigger_rules
    ALTER COLUMN trigger_kind SET NOT NULL,
    ALTER COLUMN trigger_kind SET DEFAULT 'event',
    DROP COLUMN mention_required,
    ADD CONSTRAINT trigger_rules_kind_check
        CHECK (trigger_kind IN ('event', 'label', 'slash_command', 'legacy_mention')),
    ADD CONSTRAINT trigger_rules_value_check
        CHECK (trigger_kind NOT IN ('label', 'slash_command') OR NULLIF(btrim(trigger_value), '') IS NOT NULL);

INSERT INTO trigger_rules(
    repository_id, agent_profile_id, name, event_name, action,
    trigger_kind, trigger_value, actor_min_permission, instruction_template,
    skills, allowed_tools, dangerous_actions, filters
)
SELECT repository.id, profile.id, defaults.name, defaults.event_name, 'labeled',
    'label', 'tyrs-hand', 'triage',
    'Process {{event}} {{action}} for {{owner}}/{{repository}}#{{number}} requested by {{actor}}.\n\n{{body}}',
    '[]'::jsonb, profile.allowed_tools, '[]'::jsonb, '{}'::jsonb
FROM repositories AS repository
CROSS JOIN LATERAL (
    SELECT id, allowed_tools FROM agent_profiles ORDER BY created_at LIMIT 1
) AS profile
CROSS JOIN (VALUES
    ('label-issue', 'issues'),
    ('label-pull-request', 'pull_request')
) AS defaults(name, event_name)
ON CONFLICT(repository_id, name) DO NOTHING;

ALTER TABLE job_intents
    ADD COLUMN trigger_rule_id uuid REFERENCES trigger_rules(id) ON DELETE SET NULL,
    ADD COLUMN trigger_evidence jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX idx_job_trigger_rule ON job_intents(trigger_rule_id);
