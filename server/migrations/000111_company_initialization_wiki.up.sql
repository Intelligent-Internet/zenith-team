-- The onboarding research now writes linked Company Wiki pages through
-- wiki_write, so the receipt no longer stores the research Markdown and the
-- CEO welcome Run no longer exists. completed_at is set by the Run terminal
-- path when the research Run reaches its final state.
ALTER TABLE company_initializations
    DROP COLUMN context_markdown,
    DROP COLUMN welcome_run_id;
