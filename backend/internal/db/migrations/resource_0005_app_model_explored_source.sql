-- P5 (D36): the exploration agent imports application model nodes with
-- source='explored'. Widen the existing CHECK constraint; the legacy constraint
-- keeps its plural name because D34 renames tables only.
ALTER TABLE app_model_node DROP CONSTRAINT IF EXISTS app_model_nodes_source_check;
ALTER TABLE app_model_node ADD CONSTRAINT ck_app_model_node_source
  CHECK (source IN ('manual','import','explored'));
