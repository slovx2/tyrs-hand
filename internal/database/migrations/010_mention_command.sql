ALTER TABLE trigger_rules
    DROP CONSTRAINT trigger_rules_kind_check,
    ADD CONSTRAINT trigger_rules_kind_check
        CHECK (trigger_kind IN ('event', 'label', 'slash_command', 'mention_command', 'legacy_mention'));

INSERT INTO trigger_rules(
    repository_id, agent_profile_id, name, event_name, action, enabled, priority,
    actor_min_permission, trigger_kind, trigger_value, instruction_template,
    skills, allowed_tools, dangerous_actions, filters
)
SELECT repository.id,
    COALESCE(command.agent_profile_id, profile.id),
    'mention-command', 'issue_comment', 'created', true,
    COALESCE(command.priority, 100), 'triage', 'mention_command', NULL,
    COALESCE(command.instruction_template,
        'Process {{event}} {{action}} for {{owner}}/{{repository}}#{{number}} requested by {{actor}}.\n\n{{body}}'),
    COALESCE(command.skills, '[]'::jsonb),
    COALESCE(command.allowed_tools, profile.allowed_tools),
    COALESCE(command.dangerous_actions, '[]'::jsonb),
    COALESCE(command.filters, '{}'::jsonb)
FROM repositories AS repository
CROSS JOIN LATERAL (
    SELECT id, allowed_tools FROM agent_profiles ORDER BY created_at LIMIT 1
) AS profile
LEFT JOIN LATERAL (
    SELECT agent_profile_id, priority, instruction_template, skills, allowed_tools,
        dangerous_actions, filters
    FROM trigger_rules
    WHERE repository_id = repository.id AND trigger_kind = 'slash_command'
    ORDER BY (name = 'command') DESC, priority, created_at
    LIMIT 1
) AS command ON true
ON CONFLICT(repository_id, name) DO NOTHING;
