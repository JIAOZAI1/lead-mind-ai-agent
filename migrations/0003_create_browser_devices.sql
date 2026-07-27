CREATE TABLE IF NOT EXISTS agent_browser_devices (
    id              VARCHAR(64)  PRIMARY KEY,
    user_id         VARCHAR(191) NOT NULL,
    name            VARCHAR(191) NOT NULL DEFAULT '',
    fingerprint     VARCHAR(64)  NOT NULL DEFAULT '',
    token_hash      CHAR(64)     NOT NULL,
    created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    last_seen_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    expires_at      DATETIME(3)  NOT NULL,
    revoked_at      DATETIME(3)  NULL,
    UNIQUE KEY uk_token_hash (token_hash),
    KEY idx_user_last_seen (user_id, last_seen_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
