-- The paths carrying each of a set of ipc_UUID values, in one query.
--
-- Ported from clj-icat-direct's mk-paths-for-uuids. Unlike the item query this is not
-- scoped to a user: resolving an id to a path is a lookup, and the caller checks what the
-- user may do with the path afterwards.
--
-- $1 the uuids to resolve
WITH objid AS (
    SELECT mm.object_id, m.meta_attr_value AS uuid
      FROM r_objt_metamap mm
      JOIN r_meta_main m ON m.meta_id = mm.meta_id
     WHERE m.meta_attr_name = 'ipc_UUID'
       AND m.meta_attr_value = ANY($1::text[])
)
SELECT objid.uuid, c.coll_name AS full_path
  FROM r_coll_main c
  JOIN objid ON c.coll_id = objid.object_id
UNION
SELECT DISTINCT objid.uuid, c.coll_name || '/' || d.data_name AS full_path
  FROM r_data_main d
  JOIN r_coll_main c ON d.coll_id = c.coll_id
  JOIN objid ON d.data_id = objid.object_id
