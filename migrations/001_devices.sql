CREATE TABLE devices (
    id         CHAR(36)     NOT NULL,
    name       VARCHAR(255) NOT NULL,
    brand      VARCHAR(255) NOT NULL,
    state      ENUM('available', 'in-use', 'inactive') NOT NULL,
    created_at DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_devices_brand (brand),
    KEY idx_devices_state (state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
