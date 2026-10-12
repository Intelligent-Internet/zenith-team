UPDATE runtimes SET metadata = metadata || jsonb_build_object('deviceName', COALESCE(device_name, ''));
ALTER TABLE runtimes DROP COLUMN device_name;
