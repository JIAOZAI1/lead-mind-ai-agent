CREATE TABLE IF NOT EXISTS agent_conversation_turns (
    id              BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    session_id      VARCHAR(64)  NOT NULL,
    user_id         VARCHAR(191) NOT NULL,
    role            VARCHAR(16)  NOT NULL,
    content         MEDIUMTEXT   NOT NULL,
    name            VARCHAR(191) NOT NULL DEFAULT '',
    tool_calls      JSON         NULL,
    tool_call_id    VARCHAR(191) NOT NULL DEFAULT '',
    tool_name       VARCHAR(191) NOT NULL DEFAULT '',
    created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    KEY idx_session_created (session_id, created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
