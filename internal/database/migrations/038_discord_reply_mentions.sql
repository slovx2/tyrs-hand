CREATE INDEX discord_input_messages_conversation_user
    ON discord_input_messages(conversation_id, discord_user_id);
