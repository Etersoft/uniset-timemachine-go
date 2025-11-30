-- Заполняем ClickHouse тестовыми данными для timemachine.
-- Реалистичная генерация:
-- - Дискретные (DI/DO): переключения 0↔1 каждые 10-50 сек
-- - Аналоговые (AI/AO): плавные изменения с периодическими скачками
--
-- Для генерации большего объёма данных используйте:
--   make ch-gen-data GEN_CH_DURATION=1h GEN_CH_SENSORS=1000
--
-- Этот файл генерирует минимальный набор для первичного запуска.

CREATE DATABASE IF NOT EXISTS uniset;

-- Небольшой тестовый набор: 10 AI, 10 AO, 5 DI, 5 DO
-- 2 минуты данных с шагом 1 сек для аналоговых, события по мере переключений для дискретных

INSERT INTO uniset.main_history (timestamp, value, name, nodename, producer)
WITH
    -- Аналоговые датчики: события каждую секунду
    range(1, 11) AS ai_ids,
    range(1, 11) AS ao_ids,
    toDateTime64('2024-06-01 00:00:00', 9, 'UTC') AS start_ts
SELECT
    start_ts + toIntervalSecond(step) AS ts,
    -- Базовое значение + шум + периодический скачок
    (sensor_idx * 10) + sin(step / 20.0) * 5 + (rand64() % 200) / 100.0 +
    if(step % 50 < 5, (rand64() % 2000) / 100.0 - 10, 0) AS val,
    sensor AS name,
    'node1' AS nodename,
    'seed-sql' AS producer
FROM (
    SELECT
        arrayJoin(arrayConcat(
            arrayMap(x -> concat('AI', lpad(toString(x), 4, '0'), '_S'), ai_ids),
            arrayMap(x -> concat('AO', lpad(toString(x), 4, '0'), '_C'), ao_ids)
        )) AS sensor,
        indexOf(arrayConcat(
            arrayMap(x -> concat('AI', lpad(toString(x), 4, '0'), '_S'), ai_ids),
            arrayMap(x -> concat('AO', lpad(toString(x), 4, '0'), '_C'), ao_ids)
        ), sensor) AS sensor_idx
) s
CROSS JOIN (SELECT number AS step FROM numbers(120)) steps;

-- Дискретные датчики: редкие переключения
INSERT INTO uniset.main_history (timestamp, value, name, nodename, producer)
WITH
    range(1, 6) AS di_ids,
    range(1, 6) AS do_ids,
    toDateTime64('2024-06-01 00:00:00', 9, 'UTC') AS start_ts
SELECT
    start_ts + toIntervalSecond(event_time) AS ts,
    event_value AS val,
    sensor AS name,
    'node1' AS nodename,
    'seed-sql' AS producer
FROM (
    SELECT
        sensor,
        -- Генерируем события переключения с интервалами 15-45 сек
        arrayJoin(
            arrayMap(
                i -> (
                    -- Накопленное время для события i
                    arraySum(arrayMap(j -> 15 + (rand64() % 31), range(0, i + 1))),
                    -- Значение: чередуем 0 и 1
                    if(i % 2 = 0, toFloat64(sensor_idx % 2), toFloat64(1 - sensor_idx % 2))
                ),
                range(0, 5)  -- 5 переключений на датчик
            )
        ) AS event,
        event.1 AS event_time,
        event.2 AS event_value,
        sensor_idx
    FROM (
        SELECT
            arrayJoin(arrayConcat(
                arrayMap(x -> concat('DI', lpad(toString(x), 3, '0'), '_S'), di_ids),
                arrayMap(x -> concat('DO', lpad(toString(x), 3, '0'), '_C'), do_ids)
            )) AS sensor,
            indexOf(arrayConcat(
                arrayMap(x -> concat('DI', lpad(toString(x), 3, '0'), '_S'), di_ids),
                arrayMap(x -> concat('DO', lpad(toString(x), 3, '0'), '_C'), do_ids)
            ), sensor) AS sensor_idx
    )
)
WHERE event_time < 120;  -- Ограничиваем 2 минутами
