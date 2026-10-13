-- User roles for admin-managed user provisioning (T3.3).
--
-- The server has no self-signup endpoint; the admin creates users via the
-- admin API or the CLI bootstrap. The role column distinguishes who can
-- manage users from who can only use the system.

ALTER TABLE users ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'member';
