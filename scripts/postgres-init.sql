CREATE DATABASE IF NOT EXISTS uniset;
\connect uniset
CREATE TABLE IF NOT EXISTS main_history (
    sensor_id    BIGINT       NOT NULL,
    timestamp    TIMESTAMPTZ  NOT NULL,
    time_usec    BIGINT       DEFAULT 0,
    value        DOUBLE PRECISION NOT NULL
);
CREATE INDEX IF NOT EXISTS main_history_ts_idx ON main_history (timestamp);
CREATE INDEX IF NOT EXISTS main_history_sensor_ts_idx ON main_history (sensor_id, timestamp);
