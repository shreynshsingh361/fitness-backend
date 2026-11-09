-- Steps only come from a device or a health app now: typing them in is gone, and the
-- check makes sure no typed-in count can ever be stored again.
DELETE FROM step_entries WHERE source = 'manual';
ALTER TABLE step_entries ADD CONSTRAINT step_entries_from_a_device CHECK (source <> 'manual');
