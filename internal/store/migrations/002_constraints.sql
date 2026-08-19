CREATE INDEX IF NOT EXISTS backlog_project_status_idx ON backlog_items(project_id,status);
CREATE INDEX IF NOT EXISTS projects_updated_idx ON projects(updated_at,id);
