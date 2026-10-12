-- 000008_usage_basis: stamp each usage fact with the scope its counters
-- measure. Providers that report running totals for a resumed session
-- (codex) say so explicitly instead of leaving the scope to a heuristic;
-- absent knowledge defaults to per-run counters.
ALTER TABLE run_usage
    ADD COLUMN usage_basis text DEFAULT 'per_run' NOT NULL
        CONSTRAINT run_usage_usage_basis_check
        CHECK (usage_basis IN ('per_run', 'session_cumulative'));
