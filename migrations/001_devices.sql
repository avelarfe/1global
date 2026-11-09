-- serial_number is the device's business identity. The unique index below is
-- what makes concurrent creates with the same serial race-safe: MySQL commits
-- one INSERT and rejects the other with duplicate-key error 1062.
CREATE TABLE devices (
    id            CHAR(36)     NOT NULL,
    name          VARCHAR(255) NOT NULL,
    brand         VARCHAR(255) NOT NULL,
    serial_number VARCHAR(255) NOT NULL,
    state         ENUM('available', 'in-use', 'inactive') NOT NULL,
    created_at    DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_devices_serial_number (serial_number),
    KEY idx_devices_brand (brand),
    KEY idx_devices_state (state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
