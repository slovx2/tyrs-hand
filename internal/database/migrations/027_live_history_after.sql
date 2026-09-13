ALTER TABLE live_conversations
	ADD COLUMN IF NOT EXISTS history_after timestamptz;
