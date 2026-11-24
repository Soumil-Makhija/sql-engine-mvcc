-- Fixed table kv already exists. Nothing is persisted to disk.
BEGIN;
INSERT INTO kv (id, value) VALUES (2, 'second');
INSERT INTO kv VALUES (1, 'first; with a quote: ''hello''');
EXPLAIN SELECT * FROM kv WHERE id = 1;
SELECT * FROM kv;
COMMIT;
BEGIN;
UPDATE kv SET value = 'temporary' WHERE id = 1;
DELETE FROM kv WHERE id = 2;
SELECT * FROM kv;
ROLLBACK;
SELECT * FROM kv;
