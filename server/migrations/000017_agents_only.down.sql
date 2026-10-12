DO $$ BEGIN
    RAISE EXCEPTION 'Department removal is irreversible; restore the reviewed pre-cutover backup';
END $$;
