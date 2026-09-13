ALTER TABLE codex_turn_intents DROP CONSTRAINT IF EXISTS codex_turn_intents_input_surface_check;
ALTER TABLE codex_turn_intents
	ADD CONSTRAINT codex_turn_intents_input_surface_check
	CHECK (input_surface = ANY (ARRAY['discord'::text, 'desktop'::text, 'client'::text, 'live'::text]));
