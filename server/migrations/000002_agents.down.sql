-- Reverses 000002_agents.up.sql.
DROP TABLE IF EXISTS agent_skills CASCADE;
DROP TABLE IF EXISTS skill_versions CASCADE;
DROP TABLE IF EXISTS skill_imports CASCADE;
DROP TABLE IF EXISTS agent_runtime_bindings CASCADE;
DROP TABLE IF EXISTS plugin_runtime_observations CASCADE;
DROP TABLE IF EXISTS plugin_discovery_generations CASCADE;
DROP TABLE IF EXISTS plugin_catalog_items CASCADE;
DROP TABLE IF EXISTS connector_runtime_observations CASCADE;
DROP TABLE IF EXISTS connector_discovery_generations CASCADE;
DROP TABLE IF EXISTS connector_policies CASCADE;
DROP TABLE IF EXISTS connector_catalog_items CASCADE;
DROP TABLE IF EXISTS runtimes CASCADE;
DROP TABLE IF EXISTS agents CASCADE;
