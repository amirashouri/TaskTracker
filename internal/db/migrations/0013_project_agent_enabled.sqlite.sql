-- 0013_project_agent_enabled.sqlite.sql
-- A per-project, admin-owned switch for whether an agent may be dispatched on
-- this project.
--
-- The agent section in the work-item editor greys out its "Send to agent" button
-- and shows "no agent has been set up for this project" when this is false, so a
-- member never queues a run on a project whose admin has not enabled agents. The
-- flag is project-scoped, not server-scoped: each project's admin decides.

ALTER TABLE projects ADD COLUMN agent_enabled BOOLEAN NOT NULL DEFAULT FALSE;
