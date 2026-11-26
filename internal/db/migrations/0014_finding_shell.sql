-- Whether the file of a finding is a shell script, as triage.ShellFile
-- judges it from the file's name and first line. The scan that last found
-- the finding sets it. Rows stored before a scan read first lines are
-- judged by the shell extensions alone.
ALTER TABLE finding ADD COLUMN shell boolean NOT NULL DEFAULT false;

UPDATE finding SET shell = lower(file_path) LIKE ANY ('{%.sh,%.bash,%.ksh}'::text[]);
