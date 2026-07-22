CREATE TABLE IF NOT EXISTS agent_sessions (
    id              VARCHAR(64) PRIMARY KEY,
    user_id         VARCHAR(191) NOT NULL,
    title           VARCHAR(255) NOT NULL DEFAULT '',
    pinned          TINYINT(1) NOT NULL DEFAULT 0,
    archived        TINYINT(1) NOT NULL DEFAULT 0,
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    last_active_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    KEY idx_user_list (user_id, archived, pinned, last_active_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS agent_memory_facts (
    id                  BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id             VARCHAR(191) NOT NULL,
    kind                VARCHAR(32)  NOT NULL,
    fact_key            VARCHAR(191) NOT NULL DEFAULT '',
    fact_value          TEXT NOT NULL,
    source_session_id   VARCHAR(64)  NOT NULL DEFAULT '',
    created_at          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
                        ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uq_user_kind_key (user_id, kind, fact_key),
    KEY idx_user_id (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
