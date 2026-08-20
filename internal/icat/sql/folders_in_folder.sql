-- Every subfolder of a collection, unpaged.
--
-- Ported from clj-icat-direct's list-folders-in-folder, which the navigation endpoint uses.
-- It is deliberately not the paged listing with the files filtered out: that query builds
-- CTEs over every data object in the collection before discarding them, so on a home
-- directory holding tens of thousands of files it would make a tree view far more expensive
-- than it needs to be. This touches only r_coll_main.
--
-- Unpaged, because the reference is. A folder with more subfolders than a page would
-- otherwise have its tree silently truncated.
--
-- $1 parent collection path, $2 username, $3 zone
-- $4 group ids, or NULL to derive them from $2 and $3
WITH groups AS (
    SELECT unnest($4::bigint[]) AS group_user_id
     WHERE $4::bigint[] IS NOT NULL
    UNION
    SELECT g.group_user_id
      FROM r_user_group g
     WHERE $4::bigint[] IS NULL
       AND g.user_id IN (SELECT u.user_id
                           FROM r_user_main u
                          WHERE u.user_name = $2 AND u.zone_name = $3)
)
SELECT 'collection'                           AS type,
       ca.meta_attr_value                     AS uuid,
       c.coll_name                            AS full_path,
       REGEXP_REPLACE(c.coll_name, '.*/', '') AS base_name,
       NULL                                   AS info_type,
       0                                      AS data_size,
       c.create_ts,
       c.modify_ts,
       MAX(a.access_type_id)                  AS access_type_id,
       NULL                                   AS data_checksum
  FROM r_coll_main c
  JOIN r_objt_metamap mm ON mm.object_id = c.coll_id
  JOIN r_meta_main ca ON ca.meta_id = mm.meta_id
  JOIN r_objt_access a ON c.coll_id = a.object_id
 WHERE c.parent_coll_name = $1
   -- Soft links are catalogued as collections but are not folders the DE shows.
   AND c.coll_type != 'linkPoint'
   AND ca.meta_attr_name = 'ipc_UUID'
   AND a.user_id IN (SELECT group_user_id FROM groups)
 GROUP BY type, uuid, full_path, base_name, info_type, data_size,
          c.create_ts, c.modify_ts, data_checksum
 ORDER BY base_name ASC
