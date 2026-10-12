ALTER TABLE conversation_user_states ADD COLUMN notification_level text NOT NULL DEFAULT 'all'
    CHECK (notification_level IN ('all', 'mentions', 'muted'));
