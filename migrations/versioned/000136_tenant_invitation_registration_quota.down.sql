-- Down migration for 000136_tenant_invitation_registration_quota.

ALTER TABLE tenant_invitations DROP COLUMN IF EXISTS max_registrations;
ALTER TABLE tenant_invitations DROP COLUMN IF EXISTS registered_count;
