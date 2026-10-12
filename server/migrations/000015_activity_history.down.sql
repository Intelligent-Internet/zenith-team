DO $$ BEGIN
    RAISE EXCEPTION 'Activity history cutover is irreversible; restore the reviewed pre-cutover backup';
END $$;
