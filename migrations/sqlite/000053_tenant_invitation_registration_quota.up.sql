ALTER TABLE tenant_invitations ADD COLUMN max_registrations INTEGER;
ALTER TABLE tenant_invitations ADD COLUMN registered_count INTEGER NOT NULL DEFAULT 0;
