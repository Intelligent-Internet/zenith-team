ALTER TABLE calendar_events
    DROP CONSTRAINT calendar_events_recurrence_options_check,
    DROP CONSTRAINT calendar_events_recurrence_end_check,
    DROP COLUMN recurrence_count,
    DROP COLUMN recurrence_monthly_mode,
    DROP COLUMN recurrence_weekdays,
    DROP COLUMN recurrence_interval;
