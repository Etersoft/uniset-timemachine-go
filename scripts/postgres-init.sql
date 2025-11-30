-- Database 'uniset' is created by docker-compose POSTGRES_DB env var

CREATE TABLE IF NOT EXISTS main_history (
    id BIGSERIAL PRIMARY KEY NOT NULL,
    date DATE NOT NULL,
    time TIME NOT NULL,
    time_usec INT NOT NULL CHECK (time_usec >= 0),
    sensor_id INT NOT NULL,
    value DOUBLE PRECISION NOT NULL,
    node INT NOT NULL DEFAULT 0,
    confirm INT DEFAULT NULL
);

CREATE INDEX IF NOT EXISTS main_history_sensor_date_time_idx ON main_history (sensor_id, date, time, time_usec);
CREATE INDEX IF NOT EXISTS main_history_date_time_idx ON main_history (date, time, time_usec);
