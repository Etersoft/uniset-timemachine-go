-- Заполняем ClickHouse тестовыми данными для timemachine.
-- 1000 AI (_S), 1000 AO (_C), 100 DI (_S), 100 DO (_C).
-- По каждому датчику генерируется 120 точек с шагом 5 секунд,
-- начало ряда: 2024-06-01 00:00:00 UTC.

CREATE DATABASE IF NOT EXISTS uniset;

INSERT INTO uniset.main_history (timestamp, value, name, nodename, producer)
WITH
    range(1, 1001) AS ai_ids,
    range(1, 1001) AS ao_ids,
    range(1, 101) AS di_ids,
    range(1, 101) AS do_ids,
    arrayConcat(
        arrayMap(x -> concat('AI', lpad(toString(x), 4, '0'), '_S'), ai_ids),
        arrayMap(x -> concat('AO', lpad(toString(x), 4, '0'), '_C'), ao_ids),
        arrayMap(x -> concat('DI', lpad(toString(x), 3, '0'), '_S'), di_ids),
        arrayMap(x -> concat('DO', lpad(toString(x), 3, '0'), '_C'), do_ids)
    ) AS sensors,
    toDateTime64('2024-06-01 00:00:00', 9, 'UTC') AS start_ts
SELECT
    start_ts + toIntervalSecond(step * 5) AS ts,
    if(
        startsWith(sensor, 'DI') OR startsWith(sensor, 'DO'),
        if(step % 2 = 0, 0.0, 1.0),
        10 + sin(step / 10.0) + (rand64() % 2000) / 1000.0
    ) AS val,
    sensor AS name,
    'node1' AS nodename,
    'seed-sql' AS producer
FROM (SELECT arrayJoin(sensors) AS sensor) s
CROSS JOIN (SELECT number AS step FROM numbers(120)) steps;
