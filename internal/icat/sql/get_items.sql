-- Catalog rows for a set of absolute paths, in one query.
--
-- Ported from clj-icat-direct's mk-get-item, which answered a single path. Batching is the
-- point: the bulk endpoints accept up to a thousand paths, and one round trip each costs
-- seconds where this costs milliseconds.
--
-- $1 dirnames, $2 basenames -- positionally paired, so targets reassembles the paths
-- $3 username, $4 zone
-- $5 group ids, or NULL to derive them from $3 and $4
-- $6 the AVU attribute holding a data object's info type, which is configurable
WITH targets AS (
    SELECT dirname, basename
      FROM unnest($1::text[], $2::text[]) AS t(dirname, basename)
),
groups AS (
    SELECT unnest($5::bigint[]) AS group_user_id
     WHERE $5::bigint[] IS NOT NULL
    UNION
    SELECT g.group_user_id
      FROM r_user_group g
     WHERE $5::bigint[] IS NULL
       AND g.user_id IN (SELECT u.user_id
                           FROM r_user_main u
                          WHERE u.user_name = $3 AND u.zone_name = $4)
),
objs AS (
    SELECT c.coll_id                              AS object_id,
           'collection'                           AS type,
           c.coll_name                            AS full_path,
           regexp_replace(c.coll_name, '.*/', '') AS base_name,
           0                                      AS data_size,
           c.create_ts,
           c.modify_ts,
           NULL                                   AS data_checksum
      FROM r_coll_main c
      JOIN targets t ON c.coll_name = t.dirname || '/' || t.basename
    UNION
    SELECT d.data_id,
           'dataobject',
           c.coll_name || '/' || d.data_name,
           d.data_name,
           d.data_size,
           d.create_ts,
           d.modify_ts,
           d.data_checksum
      FROM r_data_main d
      JOIN r_coll_main c ON d.coll_id = c.coll_id
      JOIN targets t ON c.coll_name = t.dirname AND d.data_name = t.basename
     -- One row per replica otherwise, which would duplicate the object.
     WHERE d.data_repl_num = (SELECT MIN(d2.data_repl_num)
                                FROM r_data_main d2
                               WHERE d2.data_id = d.data_id)
),
meta AS (
    SELECT o.object_id,
           max(CASE WHEN m.meta_attr_name = 'ipc_UUID'     THEN m.meta_attr_value END) AS uuid,
           max(CASE WHEN m.meta_attr_name = $6 THEN m.meta_attr_value END) AS info_type
      FROM objs o
      LEFT JOIN r_objt_metamap mm ON mm.object_id = o.object_id
      LEFT JOIN r_meta_main m ON m.meta_id = mm.meta_id
     GROUP BY o.object_id
)
SELECT objs.object_id,
       objs.type,
       meta.uuid,
       objs.full_path,
       objs.base_name,
       meta.info_type,
       objs.data_size,
       objs.create_ts,
       objs.modify_ts,
       max(a.access_type_id) AS access_type_id,
       objs.data_checksum
  FROM objs
  JOIN meta ON meta.object_id = objs.object_id
  JOIN r_objt_access a ON a.object_id = objs.object_id
 WHERE a.user_id IN (SELECT group_user_id FROM groups)
 GROUP BY objs.object_id, objs.type, meta.uuid, objs.full_path, objs.base_name,
          meta.info_type, objs.data_size, objs.create_ts, objs.modify_ts, objs.data_checksum
