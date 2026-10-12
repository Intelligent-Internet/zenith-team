ALTER TABLE calendar_events
    ADD COLUMN recurrence_interval integer NOT NULL DEFAULT 1 CHECK (recurrence_interval BETWEEN 1 AND 999),
    ADD COLUMN recurrence_weekdays integer[] NOT NULL DEFAULT '{}' CHECK (
        cardinality(recurrence_weekdays) <= 7
        AND recurrence_weekdays <@ ARRAY[0,1,2,3,4,5,6]
        AND array_position(recurrence_weekdays, NULL) IS NULL),
    ADD COLUMN recurrence_monthly_mode text NOT NULL DEFAULT 'day_of_month'
        CHECK (recurrence_monthly_mode IN ('day_of_month', 'day_of_week')),
    ADD COLUMN recurrence_count integer CHECK (recurrence_count BETWEEN 1 AND 1000),
    ADD CONSTRAINT calendar_events_recurrence_end_check CHECK (recurrence_count IS NULL OR recurrence_until IS NULL),
    ADD CONSTRAINT calendar_events_recurrence_options_check CHECK (
        (recurrence = 'weekly' OR cardinality(recurrence_weekdays) = 0)
        AND (recurrence = 'monthly' OR recurrence_monthly_mode = 'day_of_month')
        AND (recurrence <> 'none' OR (recurrence_interval = 1 AND recurrence_count IS NULL)));
