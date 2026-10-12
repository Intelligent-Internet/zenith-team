DO $$ BEGIN
    RAISE EXCEPTION 'Goal removal is irreversible; restore the reviewed pre-cutover backup';
END $$;
