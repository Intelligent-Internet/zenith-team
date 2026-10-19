-- Keep cancelled requests and abandoned continuations terminal on rollback.
-- Reopening them could restart provider operations without a new review.
SELECT 1;
