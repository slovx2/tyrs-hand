--
-- PostgreSQL database dump
--


-- Dumped from database version 16.10
-- Dumped by pg_dump version 16.10

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: pgcrypto; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;


--
-- Name: reconcile_conversation_running_tag(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.reconcile_conversation_running_tag() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    conversation_id uuid := NEW.discord_conversation_id;
    thread_id text;
    active boolean;
    tag_operation_key text;
    desired_payload jsonb;
BEGIN
    IF conversation_id IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT conversation.thread_id INTO thread_id
    FROM discord_conversations conversation WHERE conversation.id = conversation_id;
    IF thread_id IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT EXISTS(
        SELECT 1 FROM codex_turn_intents intent
        WHERE intent.discord_conversation_id = conversation_id
          AND (intent.status IN ('placement_pending','queued','dispatching',
                'awaiting_confirmation','running','reconciling','retry_wait')
            OR (intent.operation = 'replace_last_turn'
                AND COALESCE(intent.replacement_phase, 'reserved') <> 'terminal'))
    ) INTO active;
    tag_operation_key := 'conversation-running-tag:' || conversation_id::text;
    desired_payload := jsonb_build_object('channelId', thread_id,
        'tagName', 'Running', 'enabled', active);
    INSERT INTO integration_outbox(integration, operation_key, operation_type,
        route_key, payload)
    VALUES ('discord', tag_operation_key, 'thread.tag.toggle',
        'channels/' || thread_id || '/tags/Running', desired_payload)
    ON CONFLICT(integration, operation_key) DO UPDATE SET
        operation_type = EXCLUDED.operation_type,
        route_key = EXCLUDED.route_key,
        payload = EXCLUDED.payload,
        request_revision = integration_outbox.request_revision + 1,
        status = CASE WHEN integration_outbox.status IN ('sending','applying','ambiguous')
            THEN integration_outbox.status ELSE 'pending' END,
        attempt_count = CASE WHEN integration_outbox.status IN ('sending','applying','ambiguous')
            THEN integration_outbox.attempt_count ELSE 0 END,
        apply_attempt_count = CASE WHEN integration_outbox.status IN ('sending','applying','ambiguous')
            THEN integration_outbox.apply_attempt_count ELSE 0 END,
        available_at = now(),
        last_error = NULL,
        updated_at = now();
    RETURN NEW;
END;
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: admin_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.admin_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    administrator_id uuid NOT NULL,
    token_hash character(64) NOT NULL,
    csrf_token_hash character(64) NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    csrf_token_ciphertext bytea
);


--
-- Name: administrators; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.administrators (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    username text NOT NULL,
    password_hash text NOT NULL,
    totp_secret_ciphertext bytea NOT NULL,
    recovery_codes_hash jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: agent_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_events (
    id bigint NOT NULL,
    control_id uuid NOT NULL,
    intent_id uuid,
    run_id uuid,
    event_type text NOT NULL,
    external_event_id text,
    payload jsonb NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: agent_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.agent_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: agent_events_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.agent_events_id_seq OWNED BY public.agent_events.id;


--
-- Name: agent_profiles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_profiles (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    model text,
    reasoning_effort text,
    service_tier text,
    sandbox text DEFAULT 'workspace-write'::text NOT NULL,
    network_enabled boolean DEFAULT true NOT NULL,
    approval_policy text DEFAULT 'never'::text NOT NULL,
    allowed_tools jsonb DEFAULT '[]'::jsonb NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: audit_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.audit_logs (
    id bigint NOT NULL,
    administrator_id uuid,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text,
    request_id text,
    ip_address inet,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: audit_logs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.audit_logs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: audit_logs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.audit_logs_id_seq OWNED BY public.audit_logs.id;


--
-- Name: client_device_pairings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.client_device_pairings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    administrator_id uuid NOT NULL,
    pairing_secret_hash text NOT NULL,
    claim_token_hash text,
    device_id uuid,
    device_name text,
    platform text,
    credential_hash text,
    status text DEFAULT 'waiting_scan'::text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    claimed_at timestamp with time zone,
    confirmed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT client_device_pairings_check CHECK (((status = 'waiting_scan'::text) OR ((claim_token_hash IS NOT NULL) AND (device_id IS NOT NULL) AND (device_name IS NOT NULL) AND (platform IS NOT NULL) AND (credential_hash IS NOT NULL)))),
    CONSTRAINT client_device_pairings_status_check CHECK ((status = ANY (ARRAY['waiting_scan'::text, 'waiting_confirmation'::text, 'approved'::text, 'rejected'::text])))
);


--
-- Name: client_devices; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.client_devices (
    id uuid NOT NULL,
    administrator_id uuid NOT NULL,
    name text NOT NULL,
    platform text NOT NULL,
    credential_hash text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    approved_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone
);


--
-- Name: client_notification_outbox; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.client_notification_outbox (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    administrator_id uuid NOT NULL,
    session_id uuid NOT NULL,
    notification_type text NOT NULL,
    idempotency_key text NOT NULL,
    title text NOT NULL,
    body text NOT NULL,
    data jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    last_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    delivered_at timestamp with time zone,
    CONSTRAINT client_notification_outbox_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT client_notification_outbox_notification_type_check CHECK ((notification_type = ANY (ARRAY['run.completed'::text, 'run.failed'::text, 'interactive.required'::text]))),
    CONSTRAINT client_notification_outbox_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'sending'::text, 'retrying'::text, 'delivered'::text, 'failed'::text])))
);


--
-- Name: client_push_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.client_push_tokens (
    device_id uuid NOT NULL,
    expo_push_token text NOT NULL,
    platform text NOT NULL,
    app_environment text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    last_registered_at timestamp with time zone DEFAULT now() NOT NULL,
    last_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT client_push_tokens_app_environment_check CHECK ((app_environment = ANY (ARRAY['development'::text, 'preview'::text, 'production'::text]))),
    CONSTRAINT client_push_tokens_platform_check CHECK ((platform = ANY (ARRAY['ios'::text, 'android'::text])))
);


--
-- Name: client_updates; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.client_updates (
    cursor bigint NOT NULL,
    session_id uuid,
    update_type text NOT NULL,
    entity_id text NOT NULL,
    entity_seq bigint,
    payload jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    entity_type text,
    entity_version bigint,
    durable boolean DEFAULT true NOT NULL
);


--
-- Name: client_updates_cursor_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.client_updates_cursor_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: client_updates_cursor_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.client_updates_cursor_seq OWNED BY public.client_updates.cursor;


--
-- Name: client_user_preferences; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.client_user_preferences (
    administrator_id uuid NOT NULL,
    agent_profile_id uuid NOT NULL,
    model text,
    reasoning_effort text,
    service_tier text DEFAULT 'standard'::text NOT NULL,
    collaboration_mode text DEFAULT 'default'::text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT client_user_preferences_collaboration_mode_check CHECK ((collaboration_mode = ANY (ARRAY['default'::text, 'plan'::text]))),
    CONSTRAINT client_user_preferences_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'fast'::text]))),
    CONSTRAINT client_user_preferences_version_check CHECK ((version > 0))
);


--
-- Name: codex_interactive_requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.codex_interactive_requests (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    control_id uuid NOT NULL,
    run_id uuid NOT NULL,
    thread_id text NOT NULL,
    turn_id text NOT NULL,
    item_id text NOT NULL,
    app_server_generation bigint NOT NULL,
    app_server_request_id jsonb NOT NULL,
    questions jsonb NOT NULL,
    draft_answers jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    answer jsonb,
    answer_secret_id uuid,
    answer_surface text,
    discord_message_id text,
    deadline_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    discord_answer_message_id text,
    session_id uuid,
    CONSTRAINT codex_interactive_requests_answer_surface_check CHECK ((answer_surface = ANY (ARRAY['desktop'::text, 'discord'::text, 'client'::text, 'auto'::text]))),
    CONSTRAINT codex_interactive_requests_app_server_generation_check CHECK ((app_server_generation > 0)),
    CONSTRAINT codex_interactive_requests_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'resolved'::text, 'expired'::text, 'interrupted'::text])))
);


--
-- Name: codex_thread_controls; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.codex_thread_controls (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    source_type text NOT NULL,
    work_item_id uuid,
    discord_conversation_id uuid,
    repository_id uuid,
    agent_profile_id uuid NOT NULL,
    external_thread_id text,
    status text DEFAULT 'idle'::text NOT NULL,
    next_sequence_no bigint DEFAULT 1 NOT NULL,
    active_intent_id uuid,
    remote_status text,
    active_codex_turn_id text,
    active_client_id text,
    last_reconciled_at timestamp with time zone,
    lease_owner text,
    lease_token character(64),
    lease_epoch bigint DEFAULT 0 NOT NULL,
    lease_expires_at timestamp with time zone,
    heartbeat_at timestamp with time zone,
    next_wakeup_at timestamp with time zone,
    last_error_code text,
    last_error_message text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    model text,
    reasoning_effort text,
    service_tier text,
    runtime_preferences_frozen_at timestamp with time zone,
    worker_id uuid,
    workspace_id uuid,
    desired_thread_name text,
    desired_thread_name_source text,
    desired_thread_name_revision bigint DEFAULT 0 NOT NULL,
    applied_thread_name text,
    applied_thread_name_revision bigint DEFAULT 0 NOT NULL,
    thread_name_last_error text,
    app_server_event_generation bigint DEFAULT 0 NOT NULL,
    app_server_event_sequence bigint DEFAULT 0 NOT NULL,
    lifecycle_state text DEFAULT 'active'::text NOT NULL,
    lifecycle_revision bigint DEFAULT 0 NOT NULL,
    lifecycle_last_error text,
    app_server_lifecycle_generation bigint DEFAULT 0 NOT NULL,
    app_server_lifecycle_sequence bigint DEFAULT 0 NOT NULL,
    app_server_settings_generation bigint DEFAULT 0 NOT NULL,
    app_server_settings_sequence bigint DEFAULT 0 NOT NULL,
    workspace_project_id uuid,
    collaboration_mode text DEFAULT 'default'::text NOT NULL,
    collaboration_mode_revision bigint DEFAULT 0 NOT NULL,
    settings_revision bigint DEFAULT 0 NOT NULL,
    applied_model text,
    applied_reasoning_effort text,
    applied_service_tier text,
    applied_collaboration_mode text,
    applied_settings_revision bigint,
    settings_applied_at timestamp with time zone,
    session_id uuid,
    CONSTRAINT codex_thread_controls_app_server_event_generation_check CHECK ((app_server_event_generation >= 0)),
    CONSTRAINT codex_thread_controls_app_server_event_sequence_check CHECK ((app_server_event_sequence >= 0)),
    CONSTRAINT codex_thread_controls_app_server_lifecycle_generation_check CHECK ((app_server_lifecycle_generation >= 0)),
    CONSTRAINT codex_thread_controls_app_server_lifecycle_sequence_check CHECK ((app_server_lifecycle_sequence >= 0)),
    CONSTRAINT codex_thread_controls_app_server_settings_generation_check CHECK ((app_server_settings_generation >= 0)),
    CONSTRAINT codex_thread_controls_app_server_settings_sequence_check CHECK ((app_server_settings_sequence >= 0)),
    CONSTRAINT codex_thread_controls_applied_collaboration_mode_check CHECK (((applied_collaboration_mode IS NULL) OR (applied_collaboration_mode = ANY (ARRAY['default'::text, 'plan'::text])))),
    CONSTRAINT codex_thread_controls_applied_service_tier_check CHECK (((applied_service_tier IS NULL) OR (applied_service_tier = ANY (ARRAY['default'::text, 'priority'::text])))),
    CONSTRAINT codex_thread_controls_applied_settings_revision_check CHECK (((applied_settings_revision IS NULL) OR (applied_settings_revision >= 0))),
    CONSTRAINT codex_thread_controls_applied_thread_name_revision_check CHECK ((applied_thread_name_revision >= 0)),
    CONSTRAINT codex_thread_controls_collaboration_mode_check CHECK ((collaboration_mode = ANY (ARRAY['default'::text, 'plan'::text]))),
    CONSTRAINT codex_thread_controls_collaboration_mode_revision_check CHECK ((collaboration_mode_revision >= 0)),
    CONSTRAINT codex_thread_controls_desired_thread_name_revision_check CHECK ((desired_thread_name_revision >= 0)),
    CONSTRAINT codex_thread_controls_desired_thread_name_source_check CHECK (((desired_thread_name_source IS NULL) OR (desired_thread_name_source = ANY (ARRAY['fallback'::text, 'codex'::text])))),
    CONSTRAINT codex_thread_controls_lease_epoch_check CHECK ((lease_epoch >= 0)),
    CONSTRAINT codex_thread_controls_lifecycle_revision_check CHECK ((lifecycle_revision >= 0)),
    CONSTRAINT codex_thread_controls_lifecycle_state_check CHECK ((lifecycle_state = ANY (ARRAY['active'::text, 'archive_pending'::text, 'archived'::text, 'unarchive_pending'::text]))),
    CONSTRAINT codex_thread_controls_next_sequence_no_check CHECK ((next_sequence_no > 0)),
    CONSTRAINT codex_thread_controls_service_tier_check CHECK (((service_tier IS NULL) OR (service_tier = ANY (ARRAY['standard'::text, 'fast'::text])))),
    CONSTRAINT codex_thread_controls_settings_revision_check CHECK ((settings_revision >= 0)),
    CONSTRAINT codex_thread_controls_source_check CHECK ((((source_type = 'github_work_item'::text) AND (work_item_id IS NOT NULL) AND (session_id IS NULL) AND (discord_conversation_id IS NULL) AND (repository_id IS NOT NULL) AND (workspace_project_id IS NULL)) OR ((source_type = 'workspace_session'::text) AND (work_item_id IS NULL) AND (session_id IS NOT NULL) AND (workspace_id IS NOT NULL) AND (repository_id IS NULL) AND (workspace_project_id IS NOT NULL)))),
    CONSTRAINT codex_thread_controls_source_type_check CHECK ((source_type = ANY (ARRAY['github_work_item'::text, 'workspace_session'::text]))),
    CONSTRAINT codex_thread_controls_status_check CHECK ((status = ANY (ARRAY['idle'::text, 'dispatching'::text, 'active'::text, 'stopping'::text, 'reconciling'::text, 'error'::text])))
);


--
-- Name: codex_thread_lifecycle_requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.codex_thread_lifecycle_requests (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    control_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    source text NOT NULL,
    desired_state text NOT NULL,
    status text NOT NULL,
    revision bigint NOT NULL,
    response jsonb,
    error text,
    requested_by_discord_user_id text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    requested_by_administrator_id uuid,
    CONSTRAINT codex_thread_lifecycle_requests_desired_state_check CHECK ((desired_state = ANY (ARRAY['active'::text, 'archived'::text]))),
    CONSTRAINT codex_thread_lifecycle_requests_revision_check CHECK ((revision > 0)),
    CONSTRAINT codex_thread_lifecycle_requests_source_check CHECK ((source = ANY (ARRAY['desktop'::text, 'discord'::text, 'client'::text]))),
    CONSTRAINT codex_thread_lifecycle_requests_status_check CHECK ((status = ANY (ARRAY['waiting_for_turn'::text, 'applying'::text, 'completed'::text, 'failed'::text, 'canceled'::text])))
);


--
-- Name: codex_turn_intents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.codex_turn_intents (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    control_id uuid NOT NULL,
    sequence_no bigint NOT NULL,
    operation text DEFAULT 'turn_input'::text NOT NULL,
    behavior text,
    resolved_action text,
    target_intent_id uuid,
    source_type text NOT NULL,
    work_item_id uuid,
    discord_conversation_id uuid,
    discord_message_id text,
    repository_id uuid,
    agent_profile_id uuid NOT NULL,
    webhook_delivery_id uuid,
    trigger_rule_id uuid,
    trigger_evidence jsonb DEFAULT '{}'::jsonb NOT NULL,
    idempotency_key text NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    instruction text DEFAULT ''::text NOT NULL,
    prepared_input jsonb,
    skills jsonb DEFAULT '[]'::jsonb NOT NULL,
    allowed_tools jsonb DEFAULT '[]'::jsonb NOT NULL,
    dangerous_actions jsonb DEFAULT '[]'::jsonb NOT NULL,
    priority integer DEFAULT 100 NOT NULL,
    actor_login text DEFAULT ''::text NOT NULL,
    actor_permission text DEFAULT 'none'::text NOT NULL,
    steerable boolean DEFAULT true NOT NULL,
    codex_submission_id text,
    confirmed_codex_turn_id text,
    attempt_count integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 3 NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    last_error_code text,
    last_error_message text,
    result jsonb,
    result_delivery_status text DEFAULT 'pending'::text NOT NULL,
    result_delivery_attempt_count integer DEFAULT 0 NOT NULL,
    result_delivery_error text,
    result_delivery_token text,
    result_delivery_available_at timestamp with time zone,
    reply_policy text DEFAULT 'silent'::text NOT NULL,
    reply_status text DEFAULT 'pending'::text NOT NULL,
    reply_hook_block_count integer DEFAULT 0 NOT NULL,
    reply_tool_call_id text,
    github_comment_id bigint,
    github_comment_url text,
    dispatched_at timestamp with time zone,
    confirmed_at timestamp with time zone,
    finished_at timestamp with time zone,
    result_delivered_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    input_surface text,
    actor_participant_id uuid,
    actor_display_name text DEFAULT ''::text NOT NULL,
    desktop_input_projection_key text,
    codex_user_message_item_id text,
    desktop_input_projection_status text DEFAULT 'not_applicable'::text NOT NULL,
    workspace_project_id uuid,
    projection_anchor text,
    message_edit_revision bigint DEFAULT 0 NOT NULL,
    replacement_phase text,
    replacement_error text,
    session_id uuid,
    CONSTRAINT codex_turn_intents_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT codex_turn_intents_behavior_check CHECK ((behavior = ANY (ARRAY['start_when_idle'::text, 'steer_if_active'::text]))),
    CONSTRAINT codex_turn_intents_desktop_input_projection_status_check CHECK ((desktop_input_projection_status = ANY (ARRAY['not_applicable'::text, 'pending'::text, 'projected'::text, 'failed'::text]))),
    CONSTRAINT codex_turn_intents_input_surface_check CHECK ((input_surface = ANY (ARRAY['discord'::text, 'desktop'::text, 'client'::text]))),
    CONSTRAINT codex_turn_intents_max_attempts_check CHECK ((max_attempts > 0)),
    CONSTRAINT codex_turn_intents_message_edit_revision_check CHECK ((message_edit_revision >= 0)),
    CONSTRAINT codex_turn_intents_operation_check CHECK ((operation = ANY (ARRAY['turn_input'::text, 'interrupt'::text, 'replace_last_turn'::text]))),
    CONSTRAINT codex_turn_intents_replacement_phase_check CHECK ((replacement_phase = ANY (ARRAY['reserved'::text, 'interrupting'::text, 'rollback_pending'::text, 'rollback_applied'::text, 'start_pending'::text, 'running'::text, 'terminal'::text]))),
    CONSTRAINT codex_turn_intents_reply_hook_block_count_check CHECK ((reply_hook_block_count >= 0)),
    CONSTRAINT codex_turn_intents_reply_policy_check CHECK ((reply_policy = ANY (ARRAY['required'::text, 'silent'::text]))),
    CONSTRAINT codex_turn_intents_reply_status_check CHECK ((reply_status = ANY (ARRAY['pending'::text, 'sending'::text, 'delivered'::text, 'failed'::text, 'skipped'::text]))),
    CONSTRAINT codex_turn_intents_resolved_action_check CHECK ((resolved_action = ANY (ARRAY['start'::text, 'steer'::text, 'start_after_active'::text, 'interrupt'::text, 'replace'::text]))),
    CONSTRAINT codex_turn_intents_result_delivery_status_check CHECK ((result_delivery_status = ANY (ARRAY['pending'::text, 'delivering'::text, 'retry_wait'::text, 'delivered'::text, 'failed'::text, 'skipped'::text]))),
    CONSTRAINT codex_turn_intents_sequence_no_check CHECK ((sequence_no > 0)),
    CONSTRAINT codex_turn_intents_source_type_check CHECK ((source_type = ANY (ARRAY['github_work_item'::text, 'workspace_session'::text]))),
    CONSTRAINT codex_turn_intents_status_check CHECK ((status = ANY (ARRAY['placement_pending'::text, 'queued'::text, 'dispatching'::text, 'awaiting_confirmation'::text, 'running'::text, 'waiting_for_user'::text, 'reconciling'::text, 'retry_wait'::text, 'completed'::text, 'failed'::text, 'canceled'::text])))
);


--
-- Name: codex_turn_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.codex_turn_runs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    control_id uuid NOT NULL,
    primary_intent_id uuid NOT NULL,
    attempt integer NOT NULL,
    lease_owner text NOT NULL,
    lease_epoch bigint NOT NULL,
    capability_hash character(64) NOT NULL,
    active_slot smallint,
    status text DEFAULT 'starting'::text NOT NULL,
    codex_submission_id text,
    confirmed_codex_turn_id text,
    append_count integer DEFAULT 0 NOT NULL,
    max_append_count integer DEFAULT 5 NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    heartbeat_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    error_code text,
    error_message text,
    worker_id uuid,
    worker_event_sequence bigint DEFAULT 0 NOT NULL,
    worker_terminal_key text,
    collaboration_mode text DEFAULT 'default'::text NOT NULL,
    model text,
    reasoning_effort text,
    service_tier text,
    settings_revision bigint DEFAULT 0 NOT NULL,
    applied_model text,
    applied_reasoning_effort text,
    applied_service_tier text,
    applied_collaboration_mode text,
    applied_settings_revision bigint,
    settings_applied_at timestamp with time zone,
    codex_error jsonb,
    CONSTRAINT codex_turn_runs_active_slot_check CHECK ((active_slot = 1)),
    CONSTRAINT codex_turn_runs_applied_collaboration_mode_check CHECK (((applied_collaboration_mode IS NULL) OR (applied_collaboration_mode = ANY (ARRAY['default'::text, 'plan'::text])))),
    CONSTRAINT codex_turn_runs_applied_service_tier_check CHECK (((applied_service_tier IS NULL) OR (applied_service_tier = ANY (ARRAY['default'::text, 'priority'::text])))),
    CONSTRAINT codex_turn_runs_applied_settings_revision_check CHECK (((applied_settings_revision IS NULL) OR (applied_settings_revision >= 0))),
    CONSTRAINT codex_turn_runs_attempt_check CHECK ((attempt > 0)),
    CONSTRAINT codex_turn_runs_collaboration_mode_check CHECK ((collaboration_mode = ANY (ARRAY['default'::text, 'plan'::text]))),
    CONSTRAINT codex_turn_runs_service_tier_check CHECK (((service_tier IS NULL) OR (service_tier = ANY (ARRAY['standard'::text, 'fast'::text])))),
    CONSTRAINT codex_turn_runs_settings_revision_check CHECK ((settings_revision >= 0)),
    CONSTRAINT codex_turn_runs_status_check CHECK ((status = ANY (ARRAY['starting'::text, 'running'::text, 'waiting_for_user'::text, 'reconciling'::text, 'completed'::text, 'failed'::text, 'canceled'::text])))
);


--
-- Name: control_instances; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.control_instances (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    singleton boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT control_instances_singleton_check CHECK (singleton)
);


--
-- Name: desktop_thread_requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.desktop_thread_requests (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    operation text NOT NULL,
    request_key character(64) NOT NULL,
    source_control_id uuid,
    cwd text NOT NULL,
    request_params jsonb NOT NULL,
    status text DEFAULT 'preparing'::text NOT NULL,
    forum_id uuid,
    conversation_id uuid,
    control_id uuid,
    external_thread_id text,
    response jsonb,
    error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    first_input_projection_key text,
    first_input jsonb,
    first_input_text text,
    preview_title text,
    first_input_actor_discord_user_id text,
    first_input_actor_display_name text,
    codex_user_message_item_id text,
    CONSTRAINT desktop_thread_requests_operation_check CHECK ((operation = ANY (ARRAY['start'::text, 'fork'::text]))),
    CONSTRAINT desktop_thread_requests_status_check CHECK ((status = ANY (ARRAY['preparing'::text, 'thread_bound'::text, 'waiting_for_input'::text, 'post_pending'::text, 'completed'::text, 'post_failed'::text, 'failed'::text])))
);


--
-- Name: desktop_turn_images; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.desktop_turn_images (
    id uuid NOT NULL,
    intent_id uuid NOT NULL,
    ordinal integer NOT NULL,
    original_filename text NOT NULL,
    discord_filename text,
    media_type text,
    size_bytes bigint,
    sha256 character(64),
    status text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    discord_attachment_id text,
    error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    delivered_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT desktop_turn_images_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT desktop_turn_images_ordinal_check CHECK (((ordinal >= 0) AND (ordinal < 10))),
    CONSTRAINT desktop_turn_images_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'delivered'::text, 'failed'::text])))
);


--
-- Name: discord_attachments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_attachments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    message_id text NOT NULL,
    discord_attachment_id text NOT NULL,
    kind text NOT NULL,
    original_filename text NOT NULL,
    media_type text NOT NULL,
    size_bytes bigint NOT NULL,
    source_url text NOT NULL,
    sha256 character(64),
    relative_path text,
    status text DEFAULT 'pending'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    storage_key text,
    stored_at timestamp with time zone,
    CONSTRAINT discord_attachments_kind_check CHECK ((kind = ANY (ARRAY['image'::text, 'file'::text])))
);


--
-- Name: discord_conversations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_conversations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    guild_id text NOT NULL,
    forum_id uuid NOT NULL,
    thread_id text NOT NULL,
    starter_message_id text,
    owner_discord_user_id text NOT NULL,
    repository_id uuid,
    agent_profile_id uuid NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    last_activity_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    model text,
    reasoning_effort text,
    service_tier text,
    configuration_status text DEFAULT 'configured'::text NOT NULL,
    configuration_deadline timestamp with time zone,
    configured_by_discord_user_id text,
    title_rename_status text DEFAULT 'skipped'::text NOT NULL,
    generated_title text,
    title_renamed_at timestamp with time zone,
    lifecycle_state text DEFAULT 'active'::text NOT NULL,
    lifecycle_revision bigint DEFAULT 0 NOT NULL,
    discord_lifecycle_applied_revision bigint DEFAULT 0 NOT NULL,
    lifecycle_card_message_id text,
    lifecycle_projection_error text,
    workspace_project_id uuid,
    collaboration_mode text DEFAULT 'default'::text NOT NULL,
    collaboration_mode_revision bigint DEFAULT 0 NOT NULL,
    trigger_mode text DEFAULT 'interactive'::text NOT NULL,
    trigger_mode_revision bigint DEFAULT 0 NOT NULL,
    settings_revision bigint DEFAULT 0 NOT NULL,
    session_id uuid,
    CONSTRAINT discord_conversations_collaboration_mode_check CHECK ((collaboration_mode = ANY (ARRAY['default'::text, 'plan'::text]))),
    CONSTRAINT discord_conversations_collaboration_mode_revision_check CHECK ((collaboration_mode_revision >= 0)),
    CONSTRAINT discord_conversations_configuration_status_check CHECK ((configuration_status = ANY (ARRAY['awaiting'::text, 'editing'::text, 'configured'::text]))),
    CONSTRAINT discord_conversations_discord_lifecycle_applied_revision_check CHECK ((discord_lifecycle_applied_revision >= 0)),
    CONSTRAINT discord_conversations_lifecycle_revision_check CHECK ((lifecycle_revision >= 0)),
    CONSTRAINT discord_conversations_lifecycle_state_check CHECK ((lifecycle_state = ANY (ARRAY['active'::text, 'archive_pending'::text, 'archived'::text, 'unarchive_pending'::text]))),
    CONSTRAINT discord_conversations_scope_check CHECK ((((repository_id IS NOT NULL) AND (workspace_project_id IS NULL)) OR ((repository_id IS NULL) AND (workspace_project_id IS NOT NULL)))),
    CONSTRAINT discord_conversations_service_tier_check CHECK (((service_tier IS NULL) OR (service_tier = ANY (ARRAY['standard'::text, 'fast'::text])))),
    CONSTRAINT discord_conversations_settings_revision_check CHECK ((settings_revision >= 0)),
    CONSTRAINT discord_conversations_title_rename_status_check CHECK ((title_rename_status = ANY (ARRAY['pending'::text, 'generating'::text, 'scheduled'::text, 'completed'::text, 'failed'::text, 'skipped'::text]))),
    CONSTRAINT discord_conversations_trigger_mode_check CHECK ((trigger_mode = ANY (ARRAY['interactive'::text, 'discussion'::text]))),
    CONSTRAINT discord_conversations_trigger_mode_revision_check CHECK ((trigger_mode_revision >= 0))
);


--
-- Name: discord_forum_access; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_forum_access (
    forum_id uuid NOT NULL,
    discord_user_id text NOT NULL,
    access_level text NOT NULL,
    granted_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT discord_forum_access_access_level_check CHECK ((access_level = ANY (ARRAY['readonly'::text, 'operator'::text])))
);


--
-- Name: discord_forums; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_forums (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    guild_id text NOT NULL,
    resource_id uuid NOT NULL,
    forum_type text DEFAULT 'workspace'::text NOT NULL,
    owner_discord_user_id text,
    repository_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    workspace_id uuid,
    workspace_project_id uuid,
    binding_status text DEFAULT 'active'::text NOT NULL,
    CONSTRAINT discord_forums_binding_status_check CHECK ((binding_status = ANY (ARRAY['active'::text, 'inactive'::text]))),
    CONSTRAINT discord_forums_forum_type_check CHECK ((forum_type = ANY (ARRAY['github'::text, 'workspace'::text]))),
    CONSTRAINT discord_forums_scope_check CHECK ((((forum_type = 'github'::text) AND (owner_discord_user_id IS NULL) AND (repository_id IS NOT NULL) AND (workspace_project_id IS NULL) AND (workspace_id IS NULL)) OR ((forum_type = 'workspace'::text) AND (owner_discord_user_id IS NOT NULL) AND (repository_id IS NULL) AND (workspace_project_id IS NOT NULL) AND (workspace_id IS NOT NULL))))
);


--
-- Name: discord_gateway_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_gateway_sessions (
    guild_id text NOT NULL,
    session_id text NOT NULL,
    resume_gateway_url text NOT NULL,
    sequence bigint NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: discord_guilds; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_guilds (
    guild_id text NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    community_enabled boolean DEFAULT false NOT NULL,
    application_id text,
    bot_user_id text,
    last_gateway_status text DEFAULT 'disabled'::text NOT NULL,
    last_gateway_error text,
    last_gateway_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: discord_identity_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_identity_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    guild_id text NOT NULL,
    discord_user_id text NOT NULL,
    github_user_id bigint NOT NULL,
    github_login text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    bound_at timestamp with time zone DEFAULT now() NOT NULL,
    unbound_at timestamp with time zone
);


--
-- Name: discord_inbound_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_inbound_events (
    event_id text NOT NULL,
    guild_id text NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    status text DEFAULT 'received'::text NOT NULL,
    error text,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    processed_at timestamp with time zone
);


--
-- Name: discord_initialization_operations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_initialization_operations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    guild_id text NOT NULL,
    mode text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    requested_by uuid NOT NULL,
    preflight jsonb DEFAULT '{}'::jsonb NOT NULL,
    confirmation text,
    error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    finished_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    workspace_project_id uuid,
    CONSTRAINT discord_initialization_operations_mode_check CHECK ((mode = ANY (ARRAY['incremental'::text, 'fresh'::text])))
);


--
-- Name: discord_initialization_steps; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_initialization_steps (
    operation_id uuid NOT NULL,
    step_key text NOT NULL,
    ordinal integer NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    request jsonb DEFAULT '{}'::jsonb NOT NULL,
    result jsonb DEFAULT '{}'::jsonb NOT NULL,
    error text,
    started_at timestamp with time zone,
    finished_at timestamp with time zone
);


--
-- Name: discord_input_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_input_messages (
    message_id text NOT NULL,
    conversation_id uuid NOT NULL,
    discord_user_id text NOT NULL,
    participant_id uuid DEFAULT gen_random_uuid() NOT NULL,
    display_name text NOT NULL,
    username text NOT NULL,
    github_binding_id uuid,
    github_user_id bigint,
    github_login text,
    binding_version bigint,
    access_snapshot text NOT NULL,
    body text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'received'::text NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    processed_at timestamp with time zone,
    turn_intent_id uuid,
    edited_at timestamp with time zone,
    edit_revision bigint DEFAULT 0 NOT NULL,
    replacement_previous_intent_id uuid,
    CONSTRAINT discord_input_messages_access_snapshot_check CHECK ((access_snapshot = ANY (ARRAY['owner'::text, 'operator'::text]))),
    CONSTRAINT discord_input_messages_edit_revision_check CHECK ((edit_revision >= 0))
);


--
-- Name: discord_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_members (
    guild_id text NOT NULL,
    discord_user_id text NOT NULL,
    username text DEFAULT ''::text NOT NULL,
    display_name text DEFAULT ''::text NOT NULL,
    is_bot boolean DEFAULT false NOT NULL,
    active boolean DEFAULT true NOT NULL,
    last_synced_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: discord_oauth_states; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_oauth_states (
    state_hash character(64) NOT NULL,
    guild_id text NOT NULL,
    discord_user_id text NOT NULL,
    code_verifier_ciphertext bytea NOT NULL,
    code_verifier_nonce bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: discord_projections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_projections (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    guild_id text NOT NULL,
    projection_key text NOT NULL,
    resource_id text NOT NULL,
    message_id text,
    desired_version bigint DEFAULT 1 NOT NULL,
    applied_version bigint DEFAULT 0 NOT NULL,
    desired_payload jsonb NOT NULL,
    last_error text,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    applied_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: discord_resources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_resources (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    guild_id text NOT NULL,
    resource_key text NOT NULL,
    discord_id text NOT NULL,
    kind text NOT NULL,
    parent_discord_id text,
    name text NOT NULL,
    managed_marker text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: discord_task_posts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_task_posts (
    work_item_id uuid NOT NULL,
    forum_id uuid NOT NULL,
    thread_id text NOT NULL,
    starter_message_id text NOT NULL,
    last_state text NOT NULL,
    archived boolean DEFAULT false NOT NULL,
    last_projected_at timestamp with time zone
);


--
-- Name: discord_turn_contributors; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_turn_contributors (
    run_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    external_turn_id text,
    discord_user_id text NOT NULL,
    first_message_id text NOT NULL,
    github_binding_id uuid,
    github_user_id bigint,
    github_login text,
    binding_version bigint,
    contributed_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: discord_turn_status_cards; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_turn_status_cards (
    run_id uuid NOT NULL,
    guild_id text NOT NULL,
    projection_key text NOT NULL,
    revision bigint NOT NULL,
    role text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    boundary_client_id text,
    boundary_event_id bigint,
    CONSTRAINT discord_turn_status_cards_revision_check CHECK ((revision >= 0)),
    CONSTRAINT discord_turn_status_cards_role_check CHECK ((role = ANY (ARRAY['pending'::text, 'current'::text, 'history'::text])))
);


--
-- Name: discord_user_codex_preferences; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_user_codex_preferences (
    guild_id text NOT NULL,
    discord_user_id text NOT NULL,
    model text,
    reasoning_effort text,
    service_tier text DEFAULT 'standard'::text NOT NULL,
    collaboration_mode text DEFAULT 'default'::text NOT NULL,
    trigger_mode text DEFAULT 'interactive'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT discord_user_codex_preferences_collaboration_mode_check CHECK ((collaboration_mode = ANY (ARRAY['default'::text, 'plan'::text]))),
    CONSTRAINT discord_user_codex_preferences_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'fast'::text]))),
    CONSTRAINT discord_user_codex_preferences_trigger_mode_check CHECK ((trigger_mode = ANY (ARRAY['interactive'::text, 'discussion'::text])))
);


--
-- Name: encrypted_secrets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.encrypted_secrets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    secret_key text NOT NULL,
    key_version integer DEFAULT 1 NOT NULL,
    nonce bytea NOT NULL,
    ciphertext bytea NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: github_agent_repository_overrides; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.github_agent_repository_overrides (
    repository_id uuid NOT NULL,
    model text,
    reasoning_effort text,
    service_tier text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT github_agent_repository_overrides_service_tier_check CHECK (((service_tier IS NULL) OR (service_tier = ANY (ARRAY['standard'::text, 'fast'::text]))))
);


--
-- Name: github_app_configs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.github_app_configs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    app_id bigint NOT NULL,
    client_id text,
    app_slug text NOT NULL,
    private_key_secret_id uuid NOT NULL,
    webhook_secret_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    client_secret_secret_id uuid
);


--
-- Name: integration_outbox; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.integration_outbox (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    integration text NOT NULL,
    operation_key text NOT NULL,
    operation_type text NOT NULL,
    route_key text NOT NULL,
    payload jsonb NOT NULL,
    nonce text,
    status text DEFAULT 'pending'::text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 3 NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_token character(64),
    lease_expires_at timestamp with time zone,
    response jsonb,
    last_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    request_revision bigint DEFAULT 1 NOT NULL,
    inflight_revision bigint,
    inflight_operation_type text,
    inflight_route_key text,
    inflight_payload jsonb,
    inflight_nonce text,
    delivered_at timestamp with time zone,
    apply_attempt_count integer DEFAULT 0 NOT NULL,
    enqueue_sequence bigint NOT NULL,
    CONSTRAINT integration_outbox_apply_attempt_count_check CHECK ((apply_attempt_count >= 0)),
    CONSTRAINT integration_outbox_inflight_check CHECK ((((status = ANY (ARRAY['sending'::text, 'applying'::text, 'ambiguous'::text])) AND (inflight_revision IS NOT NULL) AND (inflight_operation_type IS NOT NULL) AND (inflight_route_key IS NOT NULL) AND (inflight_payload IS NOT NULL)) OR ((status <> ALL (ARRAY['sending'::text, 'applying'::text, 'ambiguous'::text])) AND (inflight_revision IS NULL) AND (inflight_operation_type IS NULL) AND (inflight_route_key IS NULL) AND (inflight_payload IS NULL) AND (inflight_nonce IS NULL)))),
    CONSTRAINT integration_outbox_inflight_revision_check CHECK ((inflight_revision > 0)),
    CONSTRAINT integration_outbox_request_revision_check CHECK ((request_revision > 0))
);


--
-- Name: integration_outbox_enqueue_sequence_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.integration_outbox ALTER COLUMN enqueue_sequence ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME public.integration_outbox_enqueue_sequence_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: participant_identities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.participant_identities (
    participant_id uuid NOT NULL,
    provider text NOT NULL,
    external_key text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: participants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.participants (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    kind text NOT NULL,
    display_name text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT participants_kind_check CHECK ((kind = ANY (ARRAY['administrator'::text, 'discord'::text])))
);


--
-- Name: platform_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.platform_settings (
    setting_key text NOT NULL,
    value jsonb NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: repo_caches; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repo_caches (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    path text NOT NULL,
    status text DEFAULT 'ready'::text NOT NULL,
    size_bytes bigint DEFAULT 0 NOT NULL,
    last_fetch_at timestamp with time zone,
    last_used_at timestamp with time zone DEFAULT now() NOT NULL,
    error text,
    worker_id uuid
);


--
-- Name: repositories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repositories (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    installation_id uuid NOT NULL,
    provider text NOT NULL,
    external_id bigint NOT NULL,
    owner text NOT NULL,
    name text NOT NULL,
    default_branch text NOT NULL,
    clone_url text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: scm_installations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.scm_installations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider text NOT NULL,
    external_id bigint NOT NULL,
    account_login text NOT NULL,
    account_type text NOT NULL,
    suspended_at timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: session_attachments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_attachments (
    id uuid NOT NULL,
    session_id uuid,
    uploaded_by_device_id uuid,
    source_type text NOT NULL,
    source_key text,
    kind text NOT NULL,
    original_filename text NOT NULL,
    media_type text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 character(64) NOT NULL,
    storage_key text NOT NULL,
    status text DEFAULT 'uploaded'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    attached_at timestamp with time zone,
    CONSTRAINT session_attachments_kind_check CHECK ((kind = ANY (ARRAY['image'::text, 'file'::text]))),
    CONSTRAINT session_attachments_size_bytes_check CHECK (((size_bytes >= 0) AND (size_bytes <= 26214400))),
    CONSTRAINT session_attachments_source_type_check CHECK ((source_type = ANY (ARRAY['client'::text, 'discord'::text, 'agent'::text]))),
    CONSTRAINT session_attachments_status_check CHECK ((status = ANY (ARRAY['uploaded'::text, 'attached'::text, 'deleted'::text])))
);


--
-- Name: session_message_attachments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_message_attachments (
    message_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    ordinal integer NOT NULL,
    CONSTRAINT session_message_attachments_ordinal_check CHECK ((ordinal >= 0))
);


--
-- Name: session_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_messages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    session_id uuid NOT NULL,
    seq bigint NOT NULL,
    local_id text NOT NULL,
    participant_id uuid,
    message_role text NOT NULL,
    content jsonb NOT NULL,
    source_event_id bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    turn_intent_id uuid,
    CONSTRAINT session_messages_message_role_check CHECK ((message_role = ANY (ARRAY['user'::text, 'agent'::text, 'event'::text]))),
    CONSTRAINT session_messages_seq_check CHECK ((seq > 0))
);


--
-- Name: session_surface_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_surface_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    session_id uuid NOT NULL,
    surface_type text NOT NULL,
    external_key text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: ssh_credentials; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ssh_credentials (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    secret_id uuid NOT NULL,
    public_key text NOT NULL,
    fingerprint text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: ssh_host_workers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ssh_host_workers (
    host_id uuid NOT NULL,
    worker_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: ssh_hosts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ssh_hosts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    alias text NOT NULL,
    hostname text NOT NULL,
    port integer DEFAULT 22 NOT NULL,
    username text NOT NULL,
    credential_id uuid NOT NULL,
    proxy_jump_host_id uuid,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ssh_hosts_check CHECK (((proxy_jump_host_id IS NULL) OR (proxy_jump_host_id <> id))),
    CONSTRAINT ssh_hosts_port_check CHECK (((port >= 1) AND (port <= 65535)))
);


--
-- Name: tool_calls; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_calls (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid NOT NULL,
    intent_id uuid NOT NULL,
    thread_id text NOT NULL,
    turn_id text NOT NULL,
    call_id text NOT NULL,
    namespace text NOT NULL,
    tool text NOT NULL,
    arguments jsonb NOT NULL,
    result jsonb,
    status text DEFAULT 'running'::text NOT NULL,
    error text,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    CONSTRAINT tool_calls_status_check CHECK ((status = ANY (ARRAY['running'::text, 'reconciling'::text, 'completed'::text, 'failed'::text])))
);


--
-- Name: trigger_rules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.trigger_rules (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    agent_profile_id uuid NOT NULL,
    name text NOT NULL,
    event_name text NOT NULL,
    action text,
    enabled boolean DEFAULT true NOT NULL,
    priority integer DEFAULT 100 NOT NULL,
    actor_min_permission text DEFAULT 'triage'::text NOT NULL,
    instruction_template text NOT NULL,
    skills jsonb DEFAULT '[]'::jsonb NOT NULL,
    allowed_tools jsonb DEFAULT '[]'::jsonb NOT NULL,
    dangerous_actions jsonb DEFAULT '[]'::jsonb NOT NULL,
    filters jsonb DEFAULT '{}'::jsonb NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    trigger_kind text DEFAULT 'event'::text NOT NULL,
    trigger_value text,
    CONSTRAINT trigger_rules_kind_check CHECK ((trigger_kind = ANY (ARRAY['event'::text, 'label'::text, 'slash_command'::text, 'mention_command'::text, 'legacy_mention'::text]))),
    CONSTRAINT trigger_rules_value_check CHECK (((trigger_kind <> ALL (ARRAY['label'::text, 'slash_command'::text])) OR (NULLIF(btrim(trigger_value), ''::text) IS NOT NULL)))
);


--
-- Name: webhook_deliveries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.webhook_deliveries (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider text NOT NULL,
    delivery_id text NOT NULL,
    event_name text NOT NULL,
    action text,
    signature_valid boolean NOT NULL,
    payload jsonb NOT NULL,
    status text DEFAULT 'received'::text NOT NULL,
    error text,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    processed_at timestamp with time zone
);


--
-- Name: work_item_channels; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.work_item_channels (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    work_item_id uuid NOT NULL,
    channel_type text NOT NULL,
    external_number integer NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: work_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.work_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    kind text NOT NULL,
    external_number integer NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    state text DEFAULT 'open'::text NOT NULL,
    agent_owned boolean DEFAULT false NOT NULL,
    base_sha text,
    head_sha text,
    closed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    head_ref text,
    head_repository text,
    base_ref text,
    html_url text,
    worker_id uuid
);


--
-- Name: worker_enrollments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.worker_enrollments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    worker_id uuid NOT NULL,
    token_hash character(64) NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: worker_workspaces; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.worker_workspaces (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    guild_id text NOT NULL,
    owner_discord_user_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    worker_id uuid,
    projects_scanned_at timestamp with time zone,
    project_scan_error text
);


--
-- Name: workers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.workers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    roles jsonb DEFAULT '[]'::jsonb NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    max_concurrent_jobs integer DEFAULT 6 NOT NULL,
    credential_hash character(64),
    credential_version bigint DEFAULT 0 NOT NULL,
    protocol_version integer DEFAULT 22 NOT NULL,
    worker_version text,
    status text DEFAULT 'pending'::text NOT NULL,
    heartbeat_at timestamp with time zone,
    last_error text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT workers_max_concurrent_jobs_check CHECK ((max_concurrent_jobs > 0)),
    CONSTRAINT workers_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'online'::text, 'offline'::text, 'disabled'::text, 'incompatible'::text, 'error'::text])))
);


--
-- Name: workspace_projects; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.workspace_projects (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    workspace_id uuid NOT NULL,
    relative_path text NOT NULL,
    desired_relative_path text,
    project_kind text DEFAULT 'git'::text NOT NULL,
    availability_status text DEFAULT 'available'::text NOT NULL,
    remote_url text,
    branch text,
    head_sha text,
    dirty boolean DEFAULT false NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    scan_error text,
    CONSTRAINT workspace_projects_availability_status_check CHECK ((availability_status = ANY (ARRAY['available'::text, 'missing'::text]))),
    CONSTRAINT workspace_projects_project_kind_check CHECK ((project_kind = ANY (ARRAY['directory'::text, 'git'::text]))),
    CONSTRAINT projects_name_check CHECK (((char_length(btrim(name)) >= 1) AND (char_length(btrim(name)) <= 80)))
);


--
-- Name: workspace_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.workspace_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    workspace_project_id uuid NOT NULL,
    agent_profile_id uuid NOT NULL,
    created_by_administrator_id uuid,
    title text DEFAULT ''::text NOT NULL,
    lifecycle_state text DEFAULT 'active'::text NOT NULL,
    history_completeness text DEFAULT 'complete'::text NOT NULL,
    model text,
    reasoning_effort text,
    service_tier text DEFAULT 'standard'::text NOT NULL,
    collaboration_mode text DEFAULT 'default'::text NOT NULL,
    settings_version bigint DEFAULT 0 NOT NULL,
    last_message_seq bigint DEFAULT 0 NOT NULL,
    last_activity_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    title_revision bigint DEFAULT 0 NOT NULL,
    title_source text DEFAULT 'fallback'::text NOT NULL,
    generated_title text,
    CONSTRAINT workspace_sessions_collaboration_mode_check CHECK ((collaboration_mode = ANY (ARRAY['default'::text, 'plan'::text]))),
    CONSTRAINT workspace_sessions_history_completeness_check CHECK ((history_completeness = ANY (ARRAY['complete'::text, 'partial'::text]))),
    CONSTRAINT workspace_sessions_last_message_seq_check CHECK ((last_message_seq >= 0)),
    CONSTRAINT workspace_sessions_lifecycle_state_check CHECK ((lifecycle_state = ANY (ARRAY['active'::text, 'archive_pending'::text, 'archived'::text, 'unarchive_pending'::text]))),
    CONSTRAINT workspace_sessions_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'fast'::text]))),
    CONSTRAINT workspace_sessions_settings_version_check CHECK ((settings_version >= 0)),
    CONSTRAINT workspace_sessions_title_revision_check CHECK ((title_revision >= 0)),
    CONSTRAINT workspace_sessions_title_source_check CHECK ((title_source = ANY (ARRAY['fallback'::text, 'generating'::text, 'generated'::text, 'manual'::text])))
);


--
-- Name: worktrees; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.worktrees (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    work_item_id uuid NOT NULL,
    repo_cache_id uuid NOT NULL,
    path text NOT NULL,
    branch text NOT NULL,
    base_sha text NOT NULL,
    head_sha text NOT NULL,
    status text DEFAULT 'ready'::text NOT NULL,
    dirty boolean DEFAULT false NOT NULL,
    last_used_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone,
    error text,
    worker_id uuid
);


--
-- Name: agent_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_events ALTER COLUMN id SET DEFAULT nextval('public.agent_events_id_seq'::regclass);


--
-- Name: audit_logs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_logs ALTER COLUMN id SET DEFAULT nextval('public.audit_logs_id_seq'::regclass);


--
-- Name: client_updates cursor; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_updates ALTER COLUMN cursor SET DEFAULT nextval('public.client_updates_cursor_seq'::regclass);


--
-- Name: admin_sessions admin_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.admin_sessions
    ADD CONSTRAINT admin_sessions_pkey PRIMARY KEY (id);


--
-- Name: admin_sessions admin_sessions_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.admin_sessions
    ADD CONSTRAINT admin_sessions_token_hash_key UNIQUE (token_hash);


--
-- Name: administrators administrators_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.administrators
    ADD CONSTRAINT administrators_pkey PRIMARY KEY (id);


--
-- Name: administrators administrators_username_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.administrators
    ADD CONSTRAINT administrators_username_key UNIQUE (username);


--
-- Name: agent_events agent_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_events
    ADD CONSTRAINT agent_events_pkey PRIMARY KEY (id);


--
-- Name: agent_profiles agent_profiles_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_profiles
    ADD CONSTRAINT agent_profiles_name_key UNIQUE (name);


--
-- Name: agent_profiles agent_profiles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_profiles
    ADD CONSTRAINT agent_profiles_pkey PRIMARY KEY (id);


--
-- Name: audit_logs audit_logs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_logs
    ADD CONSTRAINT audit_logs_pkey PRIMARY KEY (id);


--
-- Name: client_device_pairings client_device_pairings_claim_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_device_pairings
    ADD CONSTRAINT client_device_pairings_claim_token_hash_key UNIQUE (claim_token_hash);


--
-- Name: client_device_pairings client_device_pairings_pairing_secret_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_device_pairings
    ADD CONSTRAINT client_device_pairings_pairing_secret_hash_key UNIQUE (pairing_secret_hash);


--
-- Name: client_device_pairings client_device_pairings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_device_pairings
    ADD CONSTRAINT client_device_pairings_pkey PRIMARY KEY (id);


--
-- Name: client_devices client_devices_credential_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_devices
    ADD CONSTRAINT client_devices_credential_hash_key UNIQUE (credential_hash);


--
-- Name: client_devices client_devices_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_devices
    ADD CONSTRAINT client_devices_pkey PRIMARY KEY (id);


--
-- Name: client_notification_outbox client_notification_outbox_idempotency_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_notification_outbox
    ADD CONSTRAINT client_notification_outbox_idempotency_key_key UNIQUE (idempotency_key);


--
-- Name: client_notification_outbox client_notification_outbox_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_notification_outbox
    ADD CONSTRAINT client_notification_outbox_pkey PRIMARY KEY (id);


--
-- Name: client_push_tokens client_push_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_push_tokens
    ADD CONSTRAINT client_push_tokens_pkey PRIMARY KEY (device_id, expo_push_token);


--
-- Name: client_updates client_updates_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_updates
    ADD CONSTRAINT client_updates_pkey PRIMARY KEY (cursor);


--
-- Name: client_user_preferences client_user_preferences_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_user_preferences
    ADD CONSTRAINT client_user_preferences_pkey PRIMARY KEY (administrator_id);


--
-- Name: codex_interactive_requests codex_interactive_requests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_interactive_requests
    ADD CONSTRAINT codex_interactive_requests_pkey PRIMARY KEY (id);


--
-- Name: codex_interactive_requests codex_interactive_requests_thread_id_turn_id_item_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_interactive_requests
    ADD CONSTRAINT codex_interactive_requests_thread_id_turn_id_item_id_key UNIQUE (thread_id, turn_id, item_id);


--
-- Name: codex_thread_controls codex_thread_controls_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_thread_controls
    ADD CONSTRAINT codex_thread_controls_pkey PRIMARY KEY (id);


--
-- Name: codex_thread_lifecycle_requests codex_thread_lifecycle_requests_control_id_revision_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_thread_lifecycle_requests
    ADD CONSTRAINT codex_thread_lifecycle_requests_control_id_revision_key UNIQUE (control_id, revision);


--
-- Name: codex_thread_lifecycle_requests codex_thread_lifecycle_requests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_thread_lifecycle_requests
    ADD CONSTRAINT codex_thread_lifecycle_requests_pkey PRIMARY KEY (id);


--
-- Name: codex_turn_intents codex_turn_intents_control_id_sequence_no_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_control_id_sequence_no_key UNIQUE (control_id, sequence_no);


--
-- Name: codex_turn_intents codex_turn_intents_idempotency_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_idempotency_key_key UNIQUE (idempotency_key);


--
-- Name: codex_turn_intents codex_turn_intents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_pkey PRIMARY KEY (id);


--
-- Name: codex_turn_intents codex_turn_intents_source_check; Type: CHECK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE public.codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_source_check CHECK ((((source_type = 'github_work_item'::text) AND (work_item_id IS NOT NULL) AND (session_id IS NULL) AND (discord_conversation_id IS NULL) AND (repository_id IS NOT NULL) AND (workspace_project_id IS NULL)) OR ((source_type = 'workspace_session'::text) AND (work_item_id IS NULL) AND (session_id IS NOT NULL) AND (repository_id IS NULL) AND (workspace_project_id IS NOT NULL)))) NOT VALID;


--
-- Name: codex_turn_runs codex_turn_runs_capability_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_runs
    ADD CONSTRAINT codex_turn_runs_capability_hash_key UNIQUE (capability_hash);


--
-- Name: codex_turn_runs codex_turn_runs_control_id_active_slot_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_runs
    ADD CONSTRAINT codex_turn_runs_control_id_active_slot_key UNIQUE (control_id, active_slot);


--
-- Name: codex_turn_runs codex_turn_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_runs
    ADD CONSTRAINT codex_turn_runs_pkey PRIMARY KEY (id);


--
-- Name: codex_turn_runs codex_turn_runs_primary_intent_id_attempt_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_runs
    ADD CONSTRAINT codex_turn_runs_primary_intent_id_attempt_key UNIQUE (primary_intent_id, attempt);


--
-- Name: control_instances control_instances_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.control_instances
    ADD CONSTRAINT control_instances_pkey PRIMARY KEY (id);


--
-- Name: control_instances control_instances_singleton_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.control_instances
    ADD CONSTRAINT control_instances_singleton_key UNIQUE (singleton);


--
-- Name: desktop_thread_requests desktop_thread_requests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.desktop_thread_requests
    ADD CONSTRAINT desktop_thread_requests_pkey PRIMARY KEY (id);


--
-- Name: desktop_turn_images desktop_turn_images_intent_id_ordinal_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.desktop_turn_images
    ADD CONSTRAINT desktop_turn_images_intent_id_ordinal_key UNIQUE (intent_id, ordinal);


--
-- Name: desktop_turn_images desktop_turn_images_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.desktop_turn_images
    ADD CONSTRAINT desktop_turn_images_pkey PRIMARY KEY (id);


--
-- Name: workspace_sessions workspace_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspace_sessions
    ADD CONSTRAINT workspace_sessions_pkey PRIMARY KEY (id);


--
-- Name: discord_attachments discord_attachments_message_id_discord_attachment_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_attachments
    ADD CONSTRAINT discord_attachments_message_id_discord_attachment_id_key UNIQUE (message_id, discord_attachment_id);


--
-- Name: discord_attachments discord_attachments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_attachments
    ADD CONSTRAINT discord_attachments_pkey PRIMARY KEY (id);


--
-- Name: discord_conversations discord_conversations_guild_id_thread_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_conversations
    ADD CONSTRAINT discord_conversations_guild_id_thread_id_key UNIQUE (guild_id, thread_id);


--
-- Name: discord_conversations discord_conversations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_conversations
    ADD CONSTRAINT discord_conversations_pkey PRIMARY KEY (id);


--
-- Name: worker_workspaces worker_workspaces_guild_owner_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_workspaces
    ADD CONSTRAINT worker_workspaces_guild_owner_key UNIQUE (guild_id, owner_discord_user_id);


--
-- Name: worker_workspaces worker_workspaces_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_workspaces
    ADD CONSTRAINT worker_workspaces_pkey PRIMARY KEY (id);


--
-- Name: discord_forum_access discord_forum_access_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_forum_access
    ADD CONSTRAINT discord_forum_access_pkey PRIMARY KEY (forum_id, discord_user_id);


--
-- Name: discord_forums discord_forums_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_forums
    ADD CONSTRAINT discord_forums_pkey PRIMARY KEY (id);


--
-- Name: discord_forums discord_forums_resource_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_forums
    ADD CONSTRAINT discord_forums_resource_id_key UNIQUE (resource_id);


--
-- Name: discord_gateway_sessions discord_gateway_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_gateway_sessions
    ADD CONSTRAINT discord_gateway_sessions_pkey PRIMARY KEY (guild_id);


--
-- Name: discord_guilds discord_guilds_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_guilds
    ADD CONSTRAINT discord_guilds_pkey PRIMARY KEY (guild_id);


--
-- Name: discord_identity_bindings discord_identity_bindings_guild_id_discord_user_id_version_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_identity_bindings
    ADD CONSTRAINT discord_identity_bindings_guild_id_discord_user_id_version_key UNIQUE (guild_id, discord_user_id, version);


--
-- Name: discord_identity_bindings discord_identity_bindings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_identity_bindings
    ADD CONSTRAINT discord_identity_bindings_pkey PRIMARY KEY (id);


--
-- Name: discord_inbound_events discord_inbound_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_inbound_events
    ADD CONSTRAINT discord_inbound_events_pkey PRIMARY KEY (event_id);


--
-- Name: discord_initialization_operations discord_initialization_operations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_initialization_operations
    ADD CONSTRAINT discord_initialization_operations_pkey PRIMARY KEY (id);


--
-- Name: discord_initialization_steps discord_initialization_steps_operation_id_ordinal_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_initialization_steps
    ADD CONSTRAINT discord_initialization_steps_operation_id_ordinal_key UNIQUE (operation_id, ordinal);


--
-- Name: discord_initialization_steps discord_initialization_steps_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_initialization_steps
    ADD CONSTRAINT discord_initialization_steps_pkey PRIMARY KEY (operation_id, step_key);


--
-- Name: discord_input_messages discord_input_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_input_messages
    ADD CONSTRAINT discord_input_messages_pkey PRIMARY KEY (message_id);


--
-- Name: discord_members discord_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_members
    ADD CONSTRAINT discord_members_pkey PRIMARY KEY (guild_id, discord_user_id);


--
-- Name: discord_oauth_states discord_oauth_states_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_oauth_states
    ADD CONSTRAINT discord_oauth_states_pkey PRIMARY KEY (state_hash);


--
-- Name: discord_projections discord_projections_guild_id_projection_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_projections
    ADD CONSTRAINT discord_projections_guild_id_projection_key_key UNIQUE (guild_id, projection_key);


--
-- Name: discord_projections discord_projections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_projections
    ADD CONSTRAINT discord_projections_pkey PRIMARY KEY (id);


--
-- Name: discord_resources discord_resources_guild_id_discord_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_resources
    ADD CONSTRAINT discord_resources_guild_id_discord_id_key UNIQUE (guild_id, discord_id);


--
-- Name: discord_resources discord_resources_guild_id_resource_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_resources
    ADD CONSTRAINT discord_resources_guild_id_resource_key_key UNIQUE (guild_id, resource_key);


--
-- Name: discord_resources discord_resources_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_resources
    ADD CONSTRAINT discord_resources_pkey PRIMARY KEY (id);


--
-- Name: discord_task_posts discord_task_posts_forum_id_thread_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_task_posts
    ADD CONSTRAINT discord_task_posts_forum_id_thread_id_key UNIQUE (forum_id, thread_id);


--
-- Name: discord_task_posts discord_task_posts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_task_posts
    ADD CONSTRAINT discord_task_posts_pkey PRIMARY KEY (work_item_id);


--
-- Name: discord_turn_contributors discord_turn_contributors_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_turn_contributors
    ADD CONSTRAINT discord_turn_contributors_pkey PRIMARY KEY (run_id, discord_user_id);


--
-- Name: discord_turn_status_cards discord_turn_status_cards_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_turn_status_cards
    ADD CONSTRAINT discord_turn_status_cards_pkey PRIMARY KEY (run_id, projection_key);


--
-- Name: discord_turn_status_cards discord_turn_status_cards_run_id_revision_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_turn_status_cards
    ADD CONSTRAINT discord_turn_status_cards_run_id_revision_key UNIQUE (run_id, revision);


--
-- Name: discord_user_codex_preferences discord_user_codex_preferences_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_user_codex_preferences
    ADD CONSTRAINT discord_user_codex_preferences_pkey PRIMARY KEY (guild_id, discord_user_id);


--
-- Name: encrypted_secrets encrypted_secrets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.encrypted_secrets
    ADD CONSTRAINT encrypted_secrets_pkey PRIMARY KEY (id);


--
-- Name: encrypted_secrets encrypted_secrets_secret_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.encrypted_secrets
    ADD CONSTRAINT encrypted_secrets_secret_key_key UNIQUE (secret_key);


--
-- Name: worker_enrollments worker_enrollments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_enrollments
    ADD CONSTRAINT worker_enrollments_pkey PRIMARY KEY (id);


--
-- Name: worker_enrollments worker_enrollments_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_enrollments
    ADD CONSTRAINT worker_enrollments_token_hash_key UNIQUE (token_hash);


--
-- Name: workers workers_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workers
    ADD CONSTRAINT workers_name_key UNIQUE (name);


--
-- Name: workers workers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workers
    ADD CONSTRAINT workers_pkey PRIMARY KEY (id);


--
-- Name: github_agent_repository_overrides github_agent_repository_overrides_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_agent_repository_overrides
    ADD CONSTRAINT github_agent_repository_overrides_pkey PRIMARY KEY (repository_id);


--
-- Name: github_app_configs github_app_configs_app_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_configs
    ADD CONSTRAINT github_app_configs_app_id_key UNIQUE (app_id);


--
-- Name: github_app_configs github_app_configs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_configs
    ADD CONSTRAINT github_app_configs_pkey PRIMARY KEY (id);


--
-- Name: integration_outbox integration_outbox_integration_operation_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.integration_outbox
    ADD CONSTRAINT integration_outbox_integration_operation_key_key UNIQUE (integration, operation_key);


--
-- Name: integration_outbox integration_outbox_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.integration_outbox
    ADD CONSTRAINT integration_outbox_pkey PRIMARY KEY (id);


--
-- Name: participant_identities participant_identities_participant_id_provider_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.participant_identities
    ADD CONSTRAINT participant_identities_participant_id_provider_key UNIQUE (participant_id, provider);


--
-- Name: participant_identities participant_identities_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.participant_identities
    ADD CONSTRAINT participant_identities_pkey PRIMARY KEY (provider, external_key);


--
-- Name: participants participants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.participants
    ADD CONSTRAINT participants_pkey PRIMARY KEY (id);


--
-- Name: platform_settings platform_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.platform_settings
    ADD CONSTRAINT platform_settings_pkey PRIMARY KEY (setting_key);


--
-- Name: workspace_projects projects_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspace_projects
    ADD CONSTRAINT projects_pkey PRIMARY KEY (id);


--
-- Name: repo_caches repo_caches_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repo_caches
    ADD CONSTRAINT repo_caches_pkey PRIMARY KEY (id);


--
-- Name: repositories repositories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repositories
    ADD CONSTRAINT repositories_pkey PRIMARY KEY (id);


--
-- Name: repositories repositories_provider_external_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repositories
    ADD CONSTRAINT repositories_provider_external_id_key UNIQUE (provider, external_id);


--
-- Name: repositories repositories_provider_owner_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repositories
    ADD CONSTRAINT repositories_provider_owner_name_key UNIQUE (provider, owner, name);


--
-- Name: scm_installations scm_installations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scm_installations
    ADD CONSTRAINT scm_installations_pkey PRIMARY KEY (id);


--
-- Name: scm_installations scm_installations_provider_external_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scm_installations
    ADD CONSTRAINT scm_installations_provider_external_id_key UNIQUE (provider, external_id);


--
-- Name: session_attachments session_attachments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_attachments
    ADD CONSTRAINT session_attachments_pkey PRIMARY KEY (id);


--
-- Name: session_attachments session_attachments_source_type_source_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_attachments
    ADD CONSTRAINT session_attachments_source_type_source_key_key UNIQUE (source_type, source_key);


--
-- Name: session_attachments session_attachments_storage_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_attachments
    ADD CONSTRAINT session_attachments_storage_key_key UNIQUE (storage_key);


--
-- Name: session_message_attachments session_message_attachments_message_id_ordinal_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_message_attachments
    ADD CONSTRAINT session_message_attachments_message_id_ordinal_key UNIQUE (message_id, ordinal);


--
-- Name: session_message_attachments session_message_attachments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_message_attachments
    ADD CONSTRAINT session_message_attachments_pkey PRIMARY KEY (message_id, attachment_id);


--
-- Name: session_messages session_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_messages
    ADD CONSTRAINT session_messages_pkey PRIMARY KEY (id);


--
-- Name: session_messages session_messages_session_id_local_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_messages
    ADD CONSTRAINT session_messages_session_id_local_id_key UNIQUE (session_id, local_id);


--
-- Name: session_messages session_messages_session_id_seq_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_messages
    ADD CONSTRAINT session_messages_session_id_seq_key UNIQUE (session_id, seq);


--
-- Name: session_surface_bindings session_surface_bindings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_surface_bindings
    ADD CONSTRAINT session_surface_bindings_pkey PRIMARY KEY (id);


--
-- Name: session_surface_bindings session_surface_bindings_session_id_surface_type_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_surface_bindings
    ADD CONSTRAINT session_surface_bindings_session_id_surface_type_key UNIQUE (session_id, surface_type);


--
-- Name: session_surface_bindings session_surface_bindings_surface_type_external_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_surface_bindings
    ADD CONSTRAINT session_surface_bindings_surface_type_external_key_key UNIQUE (surface_type, external_key);


--
-- Name: ssh_credentials ssh_credentials_fingerprint_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ssh_credentials
    ADD CONSTRAINT ssh_credentials_fingerprint_key UNIQUE (fingerprint);


--
-- Name: ssh_credentials ssh_credentials_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ssh_credentials
    ADD CONSTRAINT ssh_credentials_name_key UNIQUE (name);


--
-- Name: ssh_credentials ssh_credentials_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ssh_credentials
    ADD CONSTRAINT ssh_credentials_pkey PRIMARY KEY (id);


--
-- Name: ssh_credentials ssh_credentials_secret_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ssh_credentials
    ADD CONSTRAINT ssh_credentials_secret_id_key UNIQUE (secret_id);


--
-- Name: ssh_host_workers ssh_host_workers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ssh_host_workers
    ADD CONSTRAINT ssh_host_workers_pkey PRIMARY KEY (host_id, worker_id);


--
-- Name: ssh_hosts ssh_hosts_alias_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ssh_hosts
    ADD CONSTRAINT ssh_hosts_alias_key UNIQUE (alias);


--
-- Name: ssh_hosts ssh_hosts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ssh_hosts
    ADD CONSTRAINT ssh_hosts_pkey PRIMARY KEY (id);


--
-- Name: tool_calls tool_calls_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_calls
    ADD CONSTRAINT tool_calls_pkey PRIMARY KEY (id);


--
-- Name: tool_calls tool_calls_thread_id_turn_id_call_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_calls
    ADD CONSTRAINT tool_calls_thread_id_turn_id_call_id_key UNIQUE (thread_id, turn_id, call_id);


--
-- Name: trigger_rules trigger_rules_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.trigger_rules
    ADD CONSTRAINT trigger_rules_pkey PRIMARY KEY (id);


--
-- Name: trigger_rules trigger_rules_repository_id_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.trigger_rules
    ADD CONSTRAINT trigger_rules_repository_id_name_key UNIQUE (repository_id, name);


--
-- Name: webhook_deliveries webhook_deliveries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_pkey PRIMARY KEY (id);


--
-- Name: webhook_deliveries webhook_deliveries_provider_delivery_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_provider_delivery_id_key UNIQUE (provider, delivery_id);


--
-- Name: work_item_channels work_item_channels_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_item_channels
    ADD CONSTRAINT work_item_channels_pkey PRIMARY KEY (id);


--
-- Name: work_item_channels work_item_channels_work_item_id_channel_type_external_numbe_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_item_channels
    ADD CONSTRAINT work_item_channels_work_item_id_channel_type_external_numbe_key UNIQUE (work_item_id, channel_type, external_number);


--
-- Name: work_items work_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_items
    ADD CONSTRAINT work_items_pkey PRIMARY KEY (id);


--
-- Name: work_items work_items_repository_id_kind_external_number_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_items
    ADD CONSTRAINT work_items_repository_id_kind_external_number_key UNIQUE (repository_id, kind, external_number);


--
-- Name: worktrees worktrees_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worktrees
    ADD CONSTRAINT worktrees_pkey PRIMARY KEY (id);


--
-- Name: worktrees worktrees_work_item_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worktrees
    ADD CONSTRAINT worktrees_work_item_id_key UNIQUE (work_item_id);


--
-- Name: agent_events_control; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agent_events_control ON public.agent_events USING btree (control_id, id);


--
-- Name: agent_events_run; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agent_events_run ON public.agent_events USING btree (run_id, id);


--
-- Name: agent_events_run_external_event; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX agent_events_run_external_event ON public.agent_events USING btree (run_id, external_event_id) WHERE ((run_id IS NOT NULL) AND (external_event_id IS NOT NULL));


--
-- Name: client_device_pairings_administrator; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX client_device_pairings_administrator ON public.client_device_pairings USING btree (administrator_id, created_at DESC);


--
-- Name: client_device_pairings_pending_credential; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX client_device_pairings_pending_credential ON public.client_device_pairings USING btree (credential_hash) WHERE ((credential_hash IS NOT NULL) AND (status = 'waiting_confirmation'::text));


--
-- Name: client_device_pairings_pending_device; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX client_device_pairings_pending_device ON public.client_device_pairings USING btree (device_id) WHERE ((device_id IS NOT NULL) AND (status = 'waiting_confirmation'::text));


--
-- Name: client_devices_administrator; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX client_devices_administrator ON public.client_devices USING btree (administrator_id, created_at DESC);


--
-- Name: client_notification_outbox_dispatch; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX client_notification_outbox_dispatch ON public.client_notification_outbox USING btree (status, available_at, created_at) WHERE (status = ANY (ARRAY['pending'::text, 'retrying'::text]));


--
-- Name: client_push_tokens_token; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX client_push_tokens_token ON public.client_push_tokens USING btree (expo_push_token);


--
-- Name: client_updates_created_cursor; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX client_updates_created_cursor ON public.client_updates USING btree (created_at, cursor);


--
-- Name: client_updates_retention; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX client_updates_retention ON public.client_updates USING btree (created_at);


--
-- Name: client_updates_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX client_updates_session ON public.client_updates USING btree (session_id, cursor);


--
-- Name: codex_controls_claim; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX codex_controls_claim ON public.codex_thread_controls USING btree (status, lease_expires_at, next_wakeup_at, created_at);


--
-- Name: codex_controls_discord_identity; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX codex_controls_discord_identity ON public.codex_thread_controls USING btree (discord_conversation_id) WHERE (discord_conversation_id IS NOT NULL);


--
-- Name: codex_controls_worker; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX codex_controls_worker ON public.codex_thread_controls USING btree (worker_id, status, next_wakeup_at);


--
-- Name: codex_controls_external_thread; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX codex_controls_external_thread ON public.codex_thread_controls USING btree (external_thread_id) WHERE (external_thread_id IS NOT NULL);


--
-- Name: codex_controls_github_identity; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX codex_controls_github_identity ON public.codex_thread_controls USING btree (work_item_id, agent_profile_id) WHERE (work_item_id IS NOT NULL);


--
-- Name: codex_controls_session_scope; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX codex_controls_session_scope ON public.codex_thread_controls USING btree (session_id) WHERE (session_id IS NOT NULL);


--
-- Name: codex_intents_delivery; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX codex_intents_delivery ON public.codex_turn_intents USING btree (result_delivery_status, result_delivery_available_at, finished_at);


--
-- Name: codex_intents_discord; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX codex_intents_discord ON public.codex_turn_intents USING btree (discord_conversation_id, created_at);


--
-- Name: codex_intents_queue; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX codex_intents_queue ON public.codex_turn_intents USING btree (control_id, status, available_at, sequence_no);


--
-- Name: codex_intents_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX codex_intents_session ON public.codex_turn_intents USING btree (session_id, sequence_no);


--
-- Name: codex_intents_work_item; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX codex_intents_work_item ON public.codex_turn_intents USING btree (work_item_id, created_at);


--
-- Name: codex_interactive_requests_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX codex_interactive_requests_pending ON public.codex_interactive_requests USING btree (deadline_at, created_at) WHERE (status = 'pending'::text);


--
-- Name: codex_interactive_requests_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX codex_interactive_requests_session ON public.codex_interactive_requests USING btree (session_id, created_at DESC);


--
-- Name: codex_runs_intent; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX codex_runs_intent ON public.codex_turn_runs USING btree (primary_intent_id, attempt);


--
-- Name: codex_thread_controls_workspace; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX codex_thread_controls_workspace ON public.codex_thread_controls USING btree (workspace_id, status);


--
-- Name: codex_thread_lifecycle_requests_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX codex_thread_lifecycle_requests_pending ON public.codex_thread_lifecycle_requests USING btree (workspace_id, created_at) WHERE (status = ANY (ARRAY['waiting_for_turn'::text, 'applying'::text]));


--
-- Name: codex_turn_intents_latest_submitted_input; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX codex_turn_intents_latest_submitted_input ON public.codex_turn_intents USING btree (control_id, sequence_no DESC) WHERE (operation = ANY (ARRAY['turn_input'::text, 'replace_last_turn'::text]));


--
-- Name: codex_turn_runs_worker_terminal_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX codex_turn_runs_worker_terminal_key ON public.codex_turn_runs USING btree (id, worker_terminal_key) WHERE (worker_terminal_key IS NOT NULL);


--
-- Name: desktop_thread_requests_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX desktop_thread_requests_pending ON public.desktop_thread_requests USING btree (workspace_id, created_at) WHERE (status = ANY (ARRAY['preparing'::text, 'post_pending'::text, 'codex_pending'::text]));


--
-- Name: desktop_thread_requests_pending_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX desktop_thread_requests_pending_key ON public.desktop_thread_requests USING btree (workspace_id, request_key) WHERE (status = ANY (ARRAY['preparing'::text, 'post_pending'::text, 'codex_pending'::text]));


--
-- Name: workspace_projects_workspace_path; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX workspace_projects_workspace_path ON public.workspace_projects USING btree (workspace_id, relative_path);


--
-- Name: workspace_projects_workspace_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX workspace_projects_workspace_status ON public.workspace_projects USING btree (workspace_id, availability_status, lower(name));


--
-- Name: workspace_sessions_activity; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX workspace_sessions_activity ON public.workspace_sessions USING btree (lifecycle_state, last_activity_at DESC, id DESC);


--
-- Name: workspace_sessions_workspace; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX workspace_sessions_workspace ON public.workspace_sessions USING btree (workspace_id, last_activity_at DESC);


--
-- Name: discord_attachments_storage_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX discord_attachments_storage_key ON public.discord_attachments USING btree (storage_key) WHERE (storage_key IS NOT NULL);


--
-- Name: discord_conversations_configuration_due; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX discord_conversations_configuration_due ON public.discord_conversations USING btree (configuration_deadline) WHERE (configuration_status = ANY (ARRAY['awaiting'::text, 'editing'::text]));


--
-- Name: discord_conversations_pending_title; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX discord_conversations_pending_title ON public.discord_conversations USING btree (created_at, id) WHERE (title_rename_status = 'pending'::text);


--
-- Name: discord_conversations_session_scope; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX discord_conversations_session_scope ON public.discord_conversations USING btree (session_id) WHERE (session_id IS NOT NULL);


--
-- Name: discord_forums_active_workspace_project; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX discord_forums_active_workspace_project ON public.discord_forums USING btree (workspace_project_id) WHERE ((forum_type = 'workspace'::text) AND (binding_status = 'active'::text));


--
-- Name: discord_identity_active_github; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX discord_identity_active_github ON public.discord_identity_bindings USING btree (guild_id, github_user_id) WHERE (status = 'active'::text);


--
-- Name: discord_identity_active_user; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX discord_identity_active_user ON public.discord_identity_bindings USING btree (guild_id, discord_user_id) WHERE (status = 'active'::text);


--
-- Name: discord_initialization_operations_workspace_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX discord_initialization_operations_workspace_project ON public.discord_initialization_operations USING btree (workspace_project_id, created_at DESC) WHERE (workspace_project_id IS NOT NULL);


--
-- Name: discord_input_messages_conversation_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX discord_input_messages_conversation_user ON public.discord_input_messages USING btree (conversation_id, discord_user_id);


--
-- Name: discord_input_messages_pending_batch; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX discord_input_messages_pending_batch ON public.discord_input_messages USING btree (conversation_id, received_at DESC, message_id DESC) WHERE ((status = 'received'::text) AND (turn_intent_id IS NULL));


--
-- Name: discord_input_messages_turn_intent; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX discord_input_messages_turn_intent ON public.discord_input_messages USING btree (turn_intent_id, received_at, message_id) WHERE (turn_intent_id IS NOT NULL);


--
-- Name: discord_turn_status_cards_current; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX discord_turn_status_cards_current ON public.discord_turn_status_cards USING btree (run_id) WHERE (role = 'current'::text);


--
-- Name: discord_turn_status_cards_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX discord_turn_status_cards_pending ON public.discord_turn_status_cards USING btree (guild_id, projection_key) WHERE (role = 'pending'::text);


--
-- Name: discord_turn_status_cards_unresolved_boundary; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX discord_turn_status_cards_unresolved_boundary ON public.discord_turn_status_cards USING btree (run_id, boundary_client_id) WHERE ((boundary_client_id IS NOT NULL) AND (boundary_event_id IS NULL));


--
-- Name: worker_enrollments_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX worker_enrollments_active ON public.worker_enrollments USING btree (expires_at) WHERE (consumed_at IS NULL);


--
-- Name: idx_sessions_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sessions_expiry ON public.admin_sessions USING btree (expires_at);


--
-- Name: idx_webhook_received; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_webhook_received ON public.webhook_deliveries USING btree (status, received_at);


--
-- Name: idx_worktrees_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_worktrees_expiry ON public.worktrees USING btree (expires_at) WHERE (expires_at IS NOT NULL);


--
-- Name: integration_outbox_dispatch_order; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX integration_outbox_dispatch_order ON public.integration_outbox USING btree (integration, available_at, created_at, enqueue_sequence) WHERE (status = ANY (ARRAY['pending'::text, 'retrying'::text, 'applying'::text]));


--
-- Name: integration_outbox_dispatchable; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX integration_outbox_dispatchable ON public.integration_outbox USING btree (integration, available_at, created_at) WHERE (status = ANY (ARRAY['pending'::text, 'retrying'::text, 'applying'::text]));


--
-- Name: integration_outbox_expired_delivery; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX integration_outbox_expired_delivery ON public.integration_outbox USING btree (integration, lease_expires_at) WHERE (status = 'sending'::text);


--
-- Name: repo_caches_worker_path; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX repo_caches_worker_path ON public.repo_caches USING btree (worker_id, path) WHERE (worker_id IS NOT NULL);


--
-- Name: repo_caches_worker_repository; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX repo_caches_worker_repository ON public.repo_caches USING btree (worker_id, repository_id) WHERE (worker_id IS NOT NULL);


--
-- Name: repo_caches_unplaced_repository; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX repo_caches_unplaced_repository ON public.repo_caches USING btree (repository_id) WHERE (worker_id IS NULL);


--
-- Name: session_attachments_orphans; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX session_attachments_orphans ON public.session_attachments USING btree (created_at) WHERE (status = 'uploaded'::text);


--
-- Name: session_messages_turn_intent; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX session_messages_turn_intent ON public.session_messages USING btree (turn_intent_id) WHERE (turn_intent_id IS NOT NULL);


--
-- Name: session_messages_window; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX session_messages_window ON public.session_messages USING btree (session_id, seq DESC);


--
-- Name: ssh_host_nodes_worker; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ssh_host_nodes_worker ON public.ssh_host_workers USING btree (worker_id, host_id);


--
-- Name: ssh_hosts_credential; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ssh_hosts_credential ON public.ssh_hosts USING btree (credential_id);


--
-- Name: ssh_hosts_proxy_jump; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ssh_hosts_proxy_jump ON public.ssh_hosts USING btree (proxy_jump_host_id) WHERE (proxy_jump_host_id IS NOT NULL);


--
-- Name: worker_workspaces_worker_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX worker_workspaces_worker_unique ON public.worker_workspaces USING btree (worker_id) WHERE (worker_id IS NOT NULL);


--
-- Name: worktrees_worker_path; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX worktrees_worker_path ON public.worktrees USING btree (worker_id, path) WHERE (worker_id IS NOT NULL);


--
-- Name: codex_turn_intents codex_turn_intents_running_tag; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER codex_turn_intents_running_tag AFTER INSERT OR UPDATE OF status, replacement_phase ON public.codex_turn_intents FOR EACH ROW EXECUTE FUNCTION public.reconcile_conversation_running_tag();


--
-- Name: admin_sessions admin_sessions_administrator_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.admin_sessions
    ADD CONSTRAINT admin_sessions_administrator_id_fkey FOREIGN KEY (administrator_id) REFERENCES public.administrators(id) ON DELETE CASCADE;


--
-- Name: agent_events agent_events_control_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_events
    ADD CONSTRAINT agent_events_control_id_fkey FOREIGN KEY (control_id) REFERENCES public.codex_thread_controls(id) ON DELETE CASCADE;


--
-- Name: agent_events agent_events_intent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_events
    ADD CONSTRAINT agent_events_intent_id_fkey FOREIGN KEY (intent_id) REFERENCES public.codex_turn_intents(id) ON DELETE CASCADE;


--
-- Name: agent_events agent_events_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_events
    ADD CONSTRAINT agent_events_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.codex_turn_runs(id) ON DELETE CASCADE;


--
-- Name: audit_logs audit_logs_administrator_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_logs
    ADD CONSTRAINT audit_logs_administrator_id_fkey FOREIGN KEY (administrator_id) REFERENCES public.administrators(id);


--
-- Name: client_device_pairings client_device_pairings_administrator_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_device_pairings
    ADD CONSTRAINT client_device_pairings_administrator_id_fkey FOREIGN KEY (administrator_id) REFERENCES public.administrators(id) ON DELETE CASCADE;


--
-- Name: client_devices client_devices_administrator_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_devices
    ADD CONSTRAINT client_devices_administrator_id_fkey FOREIGN KEY (administrator_id) REFERENCES public.administrators(id) ON DELETE CASCADE;


--
-- Name: client_notification_outbox client_notification_outbox_administrator_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_notification_outbox
    ADD CONSTRAINT client_notification_outbox_administrator_id_fkey FOREIGN KEY (administrator_id) REFERENCES public.administrators(id) ON DELETE CASCADE;


--
-- Name: client_notification_outbox client_notification_outbox_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_notification_outbox
    ADD CONSTRAINT client_notification_outbox_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.workspace_sessions(id) ON DELETE CASCADE;


--
-- Name: client_push_tokens client_push_tokens_device_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_push_tokens
    ADD CONSTRAINT client_push_tokens_device_id_fkey FOREIGN KEY (device_id) REFERENCES public.client_devices(id) ON DELETE CASCADE;


--
-- Name: client_updates client_updates_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_updates
    ADD CONSTRAINT client_updates_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.workspace_sessions(id) ON DELETE CASCADE;


--
-- Name: client_user_preferences client_user_preferences_administrator_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_user_preferences
    ADD CONSTRAINT client_user_preferences_administrator_id_fkey FOREIGN KEY (administrator_id) REFERENCES public.administrators(id) ON DELETE CASCADE;


--
-- Name: client_user_preferences client_user_preferences_agent_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_user_preferences
    ADD CONSTRAINT client_user_preferences_agent_profile_id_fkey FOREIGN KEY (agent_profile_id) REFERENCES public.agent_profiles(id);


--
-- Name: codex_thread_controls codex_controls_active_intent; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_thread_controls
    ADD CONSTRAINT codex_controls_active_intent FOREIGN KEY (active_intent_id) REFERENCES public.codex_turn_intents(id) ON DELETE SET NULL;


--
-- Name: codex_interactive_requests codex_interactive_requests_answer_secret_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_interactive_requests
    ADD CONSTRAINT codex_interactive_requests_answer_secret_id_fkey FOREIGN KEY (answer_secret_id) REFERENCES public.encrypted_secrets(id) ON DELETE SET NULL;


--
-- Name: codex_interactive_requests codex_interactive_requests_control_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_interactive_requests
    ADD CONSTRAINT codex_interactive_requests_control_id_fkey FOREIGN KEY (control_id) REFERENCES public.codex_thread_controls(id) ON DELETE CASCADE;


--
-- Name: codex_interactive_requests codex_interactive_requests_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_interactive_requests
    ADD CONSTRAINT codex_interactive_requests_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.codex_turn_runs(id) ON DELETE CASCADE;


--
-- Name: codex_interactive_requests codex_interactive_requests_session_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_interactive_requests
    ADD CONSTRAINT codex_interactive_requests_session_fk FOREIGN KEY (session_id) REFERENCES public.workspace_sessions(id) ON DELETE CASCADE;


--
-- Name: codex_thread_controls codex_thread_controls_agent_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_thread_controls
    ADD CONSTRAINT codex_thread_controls_agent_profile_id_fkey FOREIGN KEY (agent_profile_id) REFERENCES public.agent_profiles(id);


--
-- Name: codex_thread_controls codex_thread_controls_workspace_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_thread_controls
    ADD CONSTRAINT codex_thread_controls_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.worker_workspaces(id) ON DELETE CASCADE;


--
-- Name: codex_thread_controls codex_thread_controls_discord_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_thread_controls
    ADD CONSTRAINT codex_thread_controls_discord_conversation_id_fkey FOREIGN KEY (discord_conversation_id) REFERENCES public.discord_conversations(id) ON DELETE CASCADE;


--
-- Name: codex_thread_controls codex_thread_controls_worker_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_thread_controls
    ADD CONSTRAINT codex_thread_controls_worker_id_fkey FOREIGN KEY (worker_id) REFERENCES public.workers(id) ON DELETE RESTRICT;


--
-- Name: codex_thread_controls codex_thread_controls_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_thread_controls
    ADD CONSTRAINT codex_thread_controls_project_id_fkey FOREIGN KEY (workspace_project_id) REFERENCES public.workspace_projects(id) ON DELETE RESTRICT;


--
-- Name: codex_thread_controls codex_thread_controls_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_thread_controls
    ADD CONSTRAINT codex_thread_controls_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: codex_thread_controls codex_thread_controls_session_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_thread_controls
    ADD CONSTRAINT codex_thread_controls_session_fk FOREIGN KEY (session_id) REFERENCES public.workspace_sessions(id) ON DELETE CASCADE;


--
-- Name: codex_thread_controls codex_thread_controls_work_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_thread_controls
    ADD CONSTRAINT codex_thread_controls_work_item_id_fkey FOREIGN KEY (work_item_id) REFERENCES public.work_items(id) ON DELETE CASCADE;


--
-- Name: codex_thread_lifecycle_requests codex_thread_lifecycle_reques_requested_by_administrator_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_thread_lifecycle_requests
    ADD CONSTRAINT codex_thread_lifecycle_reques_requested_by_administrator_i_fkey FOREIGN KEY (requested_by_administrator_id) REFERENCES public.administrators(id) ON DELETE SET NULL;


--
-- Name: codex_thread_lifecycle_requests codex_thread_lifecycle_requests_control_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_thread_lifecycle_requests
    ADD CONSTRAINT codex_thread_lifecycle_requests_control_id_fkey FOREIGN KEY (control_id) REFERENCES public.codex_thread_controls(id) ON DELETE CASCADE;


--
-- Name: codex_thread_lifecycle_requests codex_thread_lifecycle_requests_workspace_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_thread_lifecycle_requests
    ADD CONSTRAINT codex_thread_lifecycle_requests_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.worker_workspaces(id) ON DELETE CASCADE;


--
-- Name: codex_turn_intents codex_turn_intents_agent_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_agent_profile_id_fkey FOREIGN KEY (agent_profile_id) REFERENCES public.agent_profiles(id);


--
-- Name: codex_turn_intents codex_turn_intents_control_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_control_id_fkey FOREIGN KEY (control_id) REFERENCES public.codex_thread_controls(id) ON DELETE CASCADE;


--
-- Name: codex_turn_intents codex_turn_intents_discord_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_discord_conversation_id_fkey FOREIGN KEY (discord_conversation_id) REFERENCES public.discord_conversations(id) ON DELETE CASCADE;


--
-- Name: codex_turn_intents codex_turn_intents_discord_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_discord_message_id_fkey FOREIGN KEY (discord_message_id) REFERENCES public.discord_input_messages(message_id) ON DELETE SET NULL;


--
-- Name: codex_turn_intents codex_turn_intents_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_project_id_fkey FOREIGN KEY (workspace_project_id) REFERENCES public.workspace_projects(id) ON DELETE RESTRICT;


--
-- Name: codex_turn_intents codex_turn_intents_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: codex_turn_intents codex_turn_intents_session_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_session_fk FOREIGN KEY (session_id) REFERENCES public.workspace_sessions(id) ON DELETE CASCADE;


--
-- Name: codex_turn_intents codex_turn_intents_target_intent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_target_intent_id_fkey FOREIGN KEY (target_intent_id) REFERENCES public.codex_turn_intents(id) ON DELETE SET NULL;


--
-- Name: codex_turn_intents codex_turn_intents_trigger_rule_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_trigger_rule_id_fkey FOREIGN KEY (trigger_rule_id) REFERENCES public.trigger_rules(id) ON DELETE SET NULL;


--
-- Name: codex_turn_intents codex_turn_intents_webhook_delivery_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_webhook_delivery_id_fkey FOREIGN KEY (webhook_delivery_id) REFERENCES public.webhook_deliveries(id);


--
-- Name: codex_turn_intents codex_turn_intents_work_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_work_item_id_fkey FOREIGN KEY (work_item_id) REFERENCES public.work_items(id) ON DELETE CASCADE;


--
-- Name: codex_turn_runs codex_turn_runs_control_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_runs
    ADD CONSTRAINT codex_turn_runs_control_id_fkey FOREIGN KEY (control_id) REFERENCES public.codex_thread_controls(id) ON DELETE CASCADE;


--
-- Name: codex_turn_runs codex_turn_runs_worker_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_runs
    ADD CONSTRAINT codex_turn_runs_worker_id_fkey FOREIGN KEY (worker_id) REFERENCES public.workers(id) ON DELETE RESTRICT;


--
-- Name: codex_turn_runs codex_turn_runs_primary_intent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.codex_turn_runs
    ADD CONSTRAINT codex_turn_runs_primary_intent_id_fkey FOREIGN KEY (primary_intent_id) REFERENCES public.codex_turn_intents(id) ON DELETE CASCADE;


--
-- Name: desktop_thread_requests desktop_thread_requests_control_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.desktop_thread_requests
    ADD CONSTRAINT desktop_thread_requests_control_id_fkey FOREIGN KEY (control_id) REFERENCES public.codex_thread_controls(id) ON DELETE SET NULL;


--
-- Name: desktop_thread_requests desktop_thread_requests_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.desktop_thread_requests
    ADD CONSTRAINT desktop_thread_requests_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.discord_conversations(id) ON DELETE SET NULL;


--
-- Name: desktop_thread_requests desktop_thread_requests_workspace_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.desktop_thread_requests
    ADD CONSTRAINT desktop_thread_requests_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.worker_workspaces(id) ON DELETE CASCADE;


--
-- Name: desktop_thread_requests desktop_thread_requests_forum_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.desktop_thread_requests
    ADD CONSTRAINT desktop_thread_requests_forum_id_fkey FOREIGN KEY (forum_id) REFERENCES public.discord_forums(id) ON DELETE SET NULL;


--
-- Name: desktop_thread_requests desktop_thread_requests_source_control_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.desktop_thread_requests
    ADD CONSTRAINT desktop_thread_requests_source_control_id_fkey FOREIGN KEY (source_control_id) REFERENCES public.codex_thread_controls(id) ON DELETE SET NULL;


--
-- Name: desktop_turn_images desktop_turn_images_intent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.desktop_turn_images
    ADD CONSTRAINT desktop_turn_images_intent_id_fkey FOREIGN KEY (intent_id) REFERENCES public.codex_turn_intents(id) ON DELETE CASCADE;


--
-- Name: workspace_projects workspace_projects_workspace_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspace_projects
    ADD CONSTRAINT workspace_projects_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.worker_workspaces(id) ON DELETE CASCADE;


--
-- Name: workspace_sessions workspace_sessions_agent_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspace_sessions
    ADD CONSTRAINT workspace_sessions_agent_profile_id_fkey FOREIGN KEY (agent_profile_id) REFERENCES public.agent_profiles(id);


--
-- Name: workspace_sessions workspace_sessions_created_by_administrator_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspace_sessions
    ADD CONSTRAINT workspace_sessions_created_by_administrator_id_fkey FOREIGN KEY (created_by_administrator_id) REFERENCES public.administrators(id) ON DELETE SET NULL;


--
-- Name: workspace_sessions workspace_sessions_workspace_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspace_sessions
    ADD CONSTRAINT workspace_sessions_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.worker_workspaces(id) ON DELETE CASCADE;


--
-- Name: workspace_sessions workspace_sessions_workspace_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspace_sessions
    ADD CONSTRAINT workspace_sessions_workspace_project_id_fkey FOREIGN KEY (workspace_project_id) REFERENCES public.workspace_projects(id) ON DELETE CASCADE;


--
-- Name: discord_attachments discord_attachments_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_attachments
    ADD CONSTRAINT discord_attachments_message_id_fkey FOREIGN KEY (message_id) REFERENCES public.discord_input_messages(message_id) ON DELETE CASCADE;


--
-- Name: discord_conversations discord_conversations_agent_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_conversations
    ADD CONSTRAINT discord_conversations_agent_profile_id_fkey FOREIGN KEY (agent_profile_id) REFERENCES public.agent_profiles(id);


--
-- Name: discord_conversations discord_conversations_forum_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_conversations
    ADD CONSTRAINT discord_conversations_forum_id_fkey FOREIGN KEY (forum_id) REFERENCES public.discord_forums(id) ON DELETE CASCADE;


--
-- Name: discord_conversations discord_conversations_guild_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_conversations
    ADD CONSTRAINT discord_conversations_guild_id_fkey FOREIGN KEY (guild_id) REFERENCES public.discord_guilds(guild_id) ON DELETE CASCADE;


--
-- Name: discord_conversations discord_conversations_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_conversations
    ADD CONSTRAINT discord_conversations_project_id_fkey FOREIGN KEY (workspace_project_id) REFERENCES public.workspace_projects(id) ON DELETE RESTRICT;


--
-- Name: discord_conversations discord_conversations_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_conversations
    ADD CONSTRAINT discord_conversations_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id);


--
-- Name: discord_conversations discord_conversations_session_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_conversations
    ADD CONSTRAINT discord_conversations_session_fk FOREIGN KEY (session_id) REFERENCES public.workspace_sessions(id) ON DELETE CASCADE;


--
-- Name: worker_workspaces worker_workspaces_worker_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_workspaces
    ADD CONSTRAINT worker_workspaces_worker_id_fkey FOREIGN KEY (worker_id) REFERENCES public.workers(id) ON DELETE RESTRICT;


--
-- Name: worker_workspaces worker_workspaces_guild_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_workspaces
    ADD CONSTRAINT worker_workspaces_guild_id_fkey FOREIGN KEY (guild_id) REFERENCES public.discord_guilds(guild_id) ON DELETE CASCADE;


--
-- Name: discord_forum_access discord_forum_access_forum_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_forum_access
    ADD CONSTRAINT discord_forum_access_forum_id_fkey FOREIGN KEY (forum_id) REFERENCES public.discord_forums(id) ON DELETE CASCADE;


--
-- Name: discord_forum_access discord_forum_access_granted_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_forum_access
    ADD CONSTRAINT discord_forum_access_granted_by_fkey FOREIGN KEY (granted_by) REFERENCES public.administrators(id);


--
-- Name: discord_forums discord_forums_workspace_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_forums
    ADD CONSTRAINT discord_forums_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.worker_workspaces(id) ON DELETE CASCADE;


--
-- Name: discord_forums discord_forums_guild_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_forums
    ADD CONSTRAINT discord_forums_guild_id_fkey FOREIGN KEY (guild_id) REFERENCES public.discord_guilds(guild_id) ON DELETE CASCADE;


--
-- Name: discord_forums discord_forums_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_forums
    ADD CONSTRAINT discord_forums_project_id_fkey FOREIGN KEY (workspace_project_id) REFERENCES public.workspace_projects(id) ON DELETE RESTRICT;


--
-- Name: discord_forums discord_forums_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_forums
    ADD CONSTRAINT discord_forums_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: discord_forums discord_forums_resource_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_forums
    ADD CONSTRAINT discord_forums_resource_id_fkey FOREIGN KEY (resource_id) REFERENCES public.discord_resources(id) ON DELETE CASCADE;


--
-- Name: discord_gateway_sessions discord_gateway_sessions_guild_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_gateway_sessions
    ADD CONSTRAINT discord_gateway_sessions_guild_id_fkey FOREIGN KEY (guild_id) REFERENCES public.discord_guilds(guild_id) ON DELETE CASCADE;


--
-- Name: discord_identity_bindings discord_identity_bindings_guild_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_identity_bindings
    ADD CONSTRAINT discord_identity_bindings_guild_id_fkey FOREIGN KEY (guild_id) REFERENCES public.discord_guilds(guild_id) ON DELETE CASCADE;


--
-- Name: discord_inbound_events discord_inbound_events_guild_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_inbound_events
    ADD CONSTRAINT discord_inbound_events_guild_id_fkey FOREIGN KEY (guild_id) REFERENCES public.discord_guilds(guild_id) ON DELETE CASCADE;


--
-- Name: discord_initialization_operations discord_initialization_operations_guild_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_initialization_operations
    ADD CONSTRAINT discord_initialization_operations_guild_id_fkey FOREIGN KEY (guild_id) REFERENCES public.discord_guilds(guild_id) ON DELETE CASCADE;


--
-- Name: discord_initialization_operations discord_initialization_operations_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_initialization_operations
    ADD CONSTRAINT discord_initialization_operations_project_id_fkey FOREIGN KEY (workspace_project_id) REFERENCES public.workspace_projects(id) ON DELETE RESTRICT;


--
-- Name: discord_initialization_operations discord_initialization_operations_requested_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_initialization_operations
    ADD CONSTRAINT discord_initialization_operations_requested_by_fkey FOREIGN KEY (requested_by) REFERENCES public.administrators(id);


--
-- Name: discord_initialization_steps discord_initialization_steps_operation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_initialization_steps
    ADD CONSTRAINT discord_initialization_steps_operation_id_fkey FOREIGN KEY (operation_id) REFERENCES public.discord_initialization_operations(id) ON DELETE CASCADE;


--
-- Name: discord_input_messages discord_input_messages_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_input_messages
    ADD CONSTRAINT discord_input_messages_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.discord_conversations(id) ON DELETE CASCADE;


--
-- Name: discord_input_messages discord_input_messages_github_binding_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_input_messages
    ADD CONSTRAINT discord_input_messages_github_binding_id_fkey FOREIGN KEY (github_binding_id) REFERENCES public.discord_identity_bindings(id);


--
-- Name: discord_input_messages discord_input_messages_replacement_previous_intent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_input_messages
    ADD CONSTRAINT discord_input_messages_replacement_previous_intent_id_fkey FOREIGN KEY (replacement_previous_intent_id) REFERENCES public.codex_turn_intents(id) ON DELETE SET NULL;


--
-- Name: discord_input_messages discord_input_messages_turn_intent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_input_messages
    ADD CONSTRAINT discord_input_messages_turn_intent_id_fkey FOREIGN KEY (turn_intent_id) REFERENCES public.codex_turn_intents(id) ON DELETE SET NULL;


--
-- Name: discord_members discord_members_guild_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_members
    ADD CONSTRAINT discord_members_guild_id_fkey FOREIGN KEY (guild_id) REFERENCES public.discord_guilds(guild_id) ON DELETE CASCADE;


--
-- Name: discord_oauth_states discord_oauth_states_guild_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_oauth_states
    ADD CONSTRAINT discord_oauth_states_guild_id_fkey FOREIGN KEY (guild_id) REFERENCES public.discord_guilds(guild_id) ON DELETE CASCADE;


--
-- Name: discord_projections discord_projections_guild_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_projections
    ADD CONSTRAINT discord_projections_guild_id_fkey FOREIGN KEY (guild_id) REFERENCES public.discord_guilds(guild_id) ON DELETE CASCADE;


--
-- Name: discord_resources discord_resources_guild_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_resources
    ADD CONSTRAINT discord_resources_guild_id_fkey FOREIGN KEY (guild_id) REFERENCES public.discord_guilds(guild_id) ON DELETE CASCADE;


--
-- Name: discord_task_posts discord_task_posts_forum_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_task_posts
    ADD CONSTRAINT discord_task_posts_forum_id_fkey FOREIGN KEY (forum_id) REFERENCES public.discord_forums(id) ON DELETE CASCADE;


--
-- Name: discord_task_posts discord_task_posts_work_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_task_posts
    ADD CONSTRAINT discord_task_posts_work_item_id_fkey FOREIGN KEY (work_item_id) REFERENCES public.work_items(id) ON DELETE CASCADE;


--
-- Name: discord_turn_contributors discord_turn_contributors_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_turn_contributors
    ADD CONSTRAINT discord_turn_contributors_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.discord_conversations(id) ON DELETE CASCADE;


--
-- Name: discord_turn_contributors discord_turn_contributors_first_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_turn_contributors
    ADD CONSTRAINT discord_turn_contributors_first_message_id_fkey FOREIGN KEY (first_message_id) REFERENCES public.discord_input_messages(message_id);


--
-- Name: discord_turn_contributors discord_turn_contributors_github_binding_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_turn_contributors
    ADD CONSTRAINT discord_turn_contributors_github_binding_id_fkey FOREIGN KEY (github_binding_id) REFERENCES public.discord_identity_bindings(id);


--
-- Name: discord_turn_contributors discord_turn_contributors_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_turn_contributors
    ADD CONSTRAINT discord_turn_contributors_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.codex_turn_runs(id) ON DELETE CASCADE;


--
-- Name: discord_turn_status_cards discord_turn_status_cards_guild_id_projection_key_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_turn_status_cards
    ADD CONSTRAINT discord_turn_status_cards_guild_id_projection_key_fkey FOREIGN KEY (guild_id, projection_key) REFERENCES public.discord_projections(guild_id, projection_key) ON DELETE CASCADE;


--
-- Name: discord_turn_status_cards discord_turn_status_cards_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_turn_status_cards
    ADD CONSTRAINT discord_turn_status_cards_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.codex_turn_runs(id) ON DELETE CASCADE;


--
-- Name: discord_user_codex_preferences discord_user_codex_preferences_guild_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_user_codex_preferences
    ADD CONSTRAINT discord_user_codex_preferences_guild_id_fkey FOREIGN KEY (guild_id) REFERENCES public.discord_guilds(guild_id) ON DELETE CASCADE;


--
-- Name: worker_enrollments worker_enrollments_worker_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_enrollments
    ADD CONSTRAINT worker_enrollments_worker_id_fkey FOREIGN KEY (worker_id) REFERENCES public.workers(id) ON DELETE CASCADE;


--
-- Name: github_agent_repository_overrides github_agent_repository_overrides_repository_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_agent_repository_overrides
    ADD CONSTRAINT github_agent_repository_overrides_repository_fk FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: github_app_configs github_app_configs_client_secret_secret_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_configs
    ADD CONSTRAINT github_app_configs_client_secret_secret_id_fkey FOREIGN KEY (client_secret_secret_id) REFERENCES public.encrypted_secrets(id);


--
-- Name: github_app_configs github_app_configs_private_key_secret_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_configs
    ADD CONSTRAINT github_app_configs_private_key_secret_id_fkey FOREIGN KEY (private_key_secret_id) REFERENCES public.encrypted_secrets(id);


--
-- Name: github_app_configs github_app_configs_webhook_secret_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.github_app_configs
    ADD CONSTRAINT github_app_configs_webhook_secret_id_fkey FOREIGN KEY (webhook_secret_id) REFERENCES public.encrypted_secrets(id);


--
-- Name: participant_identities participant_identities_participant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.participant_identities
    ADD CONSTRAINT participant_identities_participant_id_fkey FOREIGN KEY (participant_id) REFERENCES public.participants(id) ON DELETE CASCADE;


--
-- Name: repo_caches repo_caches_worker_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repo_caches
    ADD CONSTRAINT repo_caches_worker_id_fkey FOREIGN KEY (worker_id) REFERENCES public.workers(id) ON DELETE RESTRICT;


--
-- Name: repo_caches repo_caches_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repo_caches
    ADD CONSTRAINT repo_caches_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: repositories repositories_installation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repositories
    ADD CONSTRAINT repositories_installation_id_fkey FOREIGN KEY (installation_id) REFERENCES public.scm_installations(id) ON DELETE CASCADE;


--
-- Name: session_attachments session_attachments_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_attachments
    ADD CONSTRAINT session_attachments_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.workspace_sessions(id) ON DELETE CASCADE;


--
-- Name: session_attachments session_attachments_uploaded_by_device_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_attachments
    ADD CONSTRAINT session_attachments_uploaded_by_device_id_fkey FOREIGN KEY (uploaded_by_device_id) REFERENCES public.client_devices(id) ON DELETE SET NULL;


--
-- Name: session_message_attachments session_message_attachments_attachment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_message_attachments
    ADD CONSTRAINT session_message_attachments_attachment_id_fkey FOREIGN KEY (attachment_id) REFERENCES public.session_attachments(id) ON DELETE CASCADE;


--
-- Name: session_message_attachments session_message_attachments_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_message_attachments
    ADD CONSTRAINT session_message_attachments_message_id_fkey FOREIGN KEY (message_id) REFERENCES public.session_messages(id) ON DELETE CASCADE;


--
-- Name: session_messages session_messages_participant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_messages
    ADD CONSTRAINT session_messages_participant_id_fkey FOREIGN KEY (participant_id) REFERENCES public.participants(id) ON DELETE SET NULL;


--
-- Name: session_messages session_messages_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_messages
    ADD CONSTRAINT session_messages_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.workspace_sessions(id) ON DELETE CASCADE;


--
-- Name: session_messages session_messages_source_event_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_messages
    ADD CONSTRAINT session_messages_source_event_id_fkey FOREIGN KEY (source_event_id) REFERENCES public.agent_events(id) ON DELETE SET NULL;


--
-- Name: session_messages session_messages_turn_intent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_messages
    ADD CONSTRAINT session_messages_turn_intent_id_fkey FOREIGN KEY (turn_intent_id) REFERENCES public.codex_turn_intents(id) ON DELETE SET NULL;


--
-- Name: session_surface_bindings session_surface_bindings_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_surface_bindings
    ADD CONSTRAINT session_surface_bindings_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.workspace_sessions(id) ON DELETE CASCADE;


--
-- Name: ssh_credentials ssh_credentials_secret_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ssh_credentials
    ADD CONSTRAINT ssh_credentials_secret_id_fkey FOREIGN KEY (secret_id) REFERENCES public.encrypted_secrets(id) ON DELETE RESTRICT;


--
-- Name: ssh_host_workers ssh_host_workers_worker_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ssh_host_workers
    ADD CONSTRAINT ssh_host_workers_worker_id_fkey FOREIGN KEY (worker_id) REFERENCES public.workers(id) ON DELETE CASCADE;


--
-- Name: ssh_host_workers ssh_host_workers_host_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ssh_host_workers
    ADD CONSTRAINT ssh_host_workers_host_id_fkey FOREIGN KEY (host_id) REFERENCES public.ssh_hosts(id) ON DELETE CASCADE;


--
-- Name: ssh_hosts ssh_hosts_credential_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ssh_hosts
    ADD CONSTRAINT ssh_hosts_credential_id_fkey FOREIGN KEY (credential_id) REFERENCES public.ssh_credentials(id) ON DELETE RESTRICT;


--
-- Name: ssh_hosts ssh_hosts_proxy_jump_host_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ssh_hosts
    ADD CONSTRAINT ssh_hosts_proxy_jump_host_id_fkey FOREIGN KEY (proxy_jump_host_id) REFERENCES public.ssh_hosts(id) ON DELETE RESTRICT;


--
-- Name: tool_calls tool_calls_intent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_calls
    ADD CONSTRAINT tool_calls_intent_id_fkey FOREIGN KEY (intent_id) REFERENCES public.codex_turn_intents(id) ON DELETE CASCADE;


--
-- Name: tool_calls tool_calls_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_calls
    ADD CONSTRAINT tool_calls_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.codex_turn_runs(id) ON DELETE CASCADE;


--
-- Name: trigger_rules trigger_rules_agent_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.trigger_rules
    ADD CONSTRAINT trigger_rules_agent_profile_id_fkey FOREIGN KEY (agent_profile_id) REFERENCES public.agent_profiles(id);


--
-- Name: trigger_rules trigger_rules_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.trigger_rules
    ADD CONSTRAINT trigger_rules_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: work_item_channels work_item_channels_work_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_item_channels
    ADD CONSTRAINT work_item_channels_work_item_id_fkey FOREIGN KEY (work_item_id) REFERENCES public.work_items(id) ON DELETE CASCADE;


--
-- Name: work_items work_items_worker_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_items
    ADD CONSTRAINT work_items_worker_id_fkey FOREIGN KEY (worker_id) REFERENCES public.workers(id) ON DELETE RESTRICT;


--
-- Name: work_items work_items_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_items
    ADD CONSTRAINT work_items_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: worktrees worktrees_worker_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worktrees
    ADD CONSTRAINT worktrees_worker_id_fkey FOREIGN KEY (worker_id) REFERENCES public.workers(id) ON DELETE RESTRICT;


--
-- Name: worktrees worktrees_repo_cache_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worktrees
    ADD CONSTRAINT worktrees_repo_cache_id_fkey FOREIGN KEY (repo_cache_id) REFERENCES public.repo_caches(id) ON DELETE CASCADE;


--
-- Name: worktrees worktrees_work_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worktrees
    ADD CONSTRAINT worktrees_work_item_id_fkey FOREIGN KEY (work_item_id) REFERENCES public.work_items(id) ON DELETE CASCADE;


-- Fresh installations always have one GitHub Agent profile available for rules and sessions.
INSERT INTO public.agent_profiles(name, allowed_tools)
VALUES (
    'Default',
    '["add_issue_comment","create_pull_request","get_commit","get_file_contents","issue_read","label_write","list_branches","list_commits","pull_request_read","pull_request_review_write","request_pull_request_reviewers"]'::jsonb
);

INSERT INTO public.control_instances(singleton) VALUES (true);


--
-- PostgreSQL database dump complete
--
