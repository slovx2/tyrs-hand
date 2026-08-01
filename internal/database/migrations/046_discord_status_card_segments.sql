ALTER TABLE discord_turn_status_cards
    ADD COLUMN boundary_client_id text,
    ADD COLUMN boundary_event_id bigint;

CREATE INDEX discord_turn_status_cards_unresolved_boundary
    ON discord_turn_status_cards(run_id, boundary_client_id)
    WHERE boundary_client_id IS NOT NULL AND boundary_event_id IS NULL;
