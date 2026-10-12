-- Reverse of 000005_execution: shed the column added to 000003's table, then
-- drop the execution machine children-first. conversation_items needs no
-- unwinding: its run_id was never a foreign key.
ALTER TABLE agents DROP COLUMN IF EXISTS runtime_id;
ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_schedule_firing_fkey;
ALTER TABLE conversations DROP CONSTRAINT IF EXISTS conversations_schedule_id_fkey;
DROP TABLE IF EXISTS task_report_events;
DROP TABLE IF EXISTS task_report_batches;
DROP TABLE IF EXISTS task_merge_jobs;
DROP TABLE IF EXISTS schedule_firings;
DROP TABLE IF EXISTS schedule_sources;
DROP TABLE IF EXISTS schedules;
DROP TABLE IF EXISTS plugin_runtime_observations;
DROP TABLE IF EXISTS plugin_discovery_generations;
DROP TABLE IF EXISTS plugin_catalog_items;
DROP TABLE IF EXISTS connector_runtime_observations;
DROP TABLE IF EXISTS connector_discovery_generations;
DROP TABLE IF EXISTS connector_policies;
DROP TABLE IF EXISTS connector_catalog_items;
DROP TABLE IF EXISTS questions;
DROP TABLE IF EXISTS approvals;
DROP TABLE IF EXISTS run_tokens;
DROP TABLE IF EXISTS runs;
DROP TABLE IF EXISTS runtimes;
