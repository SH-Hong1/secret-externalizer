-- The last time each worker asked the queue for a job. The web program reads
-- it to tell a queue that is waiting for a free worker from one that no
-- worker is serving.
CREATE TABLE worker_beat (
    worker_id text PRIMARY KEY,
    seen_at   timestamptz NOT NULL
);
