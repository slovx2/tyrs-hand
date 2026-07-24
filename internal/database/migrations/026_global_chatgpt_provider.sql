UPDATE platform_settings
SET value = jsonb_strip_nulls(jsonb_build_object(
    'modelSource', CASE
        WHEN value->>'providerType' = 'device-code' THEN 'chatgpt'
        ELSE 'provider'
    END,
    'baseUrl', value->>'baseUrl',
    'model', value->>'model',
    'reasoningEffort', value->>'reasoningEffort',
    'serviceTier', value->>'serviceTier',
    'proxyUrl', value->>'proxyUrl',
    'providerConfigured', COALESCE(value->>'credentialVersion', '') <> '',
    'chatgptConfigured', COALESCE((value->>'providerType' = 'device-code')
        AND (value->>'configured')::boolean, false),
    'chatgptAuthRevision', CASE
        WHEN COALESCE((value->>'providerType' = 'device-code')
            AND (value->>'configured')::boolean, false) THEN 1
        ELSE 0
    END,
    'configSignature', value->>'configSignature',
    'credentialVersion', value->>'credentialVersion'
))
WHERE setting_key = 'agent.provider'
  AND value ? 'providerType';

CREATE TABLE codex_auth_operations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    login_id text,
    auth_url text,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','awaiting_user','completed','failed','canceled')),
    account_email text,
    account_plan_type text,
    error text,
    requested_by uuid REFERENCES administrators(id) ON DELETE SET NULL,
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);

CREATE UNIQUE INDEX codex_auth_operations_active
    ON codex_auth_operations ((true))
    WHERE status IN ('pending','awaiting_user');
