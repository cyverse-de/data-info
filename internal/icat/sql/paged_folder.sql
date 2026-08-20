-- A sorted, paged listing of a collection's immediate children.
--
-- Ported from clj-icat-direct's mk-paged-folder, which ran this as four statements in a
-- transaction: two temporary tables with an ANALYZE each, then the listing. The temporary
-- tables existed to stop PostgreSQL mis-estimating a CTE's cardinality and choosing nested
-- loops over the catalog. Modern PostgreSQL materialises a CTE referenced more than once and
-- can inline one referenced once, so the same shape is expressed here as CTEs and measured
-- rather than assumed; if a plan regresses, the temporary tables are the fallback.
--
-- Rows are ordered by type first so that folders precede files, then by the requested
-- column. The order is the contract -- it is what a sort-field request is asking for -- so
-- nothing downstream may reorder it.
--
-- $1 parent collection path
-- $2 username, $3 zone
-- $4 group ids, or NULL to derive them from $2 and $3
-- $5 info types to keep, or NULL for all
-- $6 whether an object with no info type is kept when $5 is set
-- $7 limit, $8 offset
-- The sort column and direction are interpolated by the caller from a fixed table; they are
-- identifiers and cannot be parameters.
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
),
objs AS (
    -- One row per data object rather than per replica.
    SELECT d.*
      FROM r_data_main d
     WHERE d.coll_id = (SELECT coll_id FROM r_coll_main WHERE coll_name = $1)
       AND d.data_repl_num = (SELECT MIN(d2.data_repl_num)
                                FROM r_data_main d2
                               WHERE d2.data_id = d.data_id)
),
avus AS (
    SELECT mm.object_id, m.meta_attr_value, m.meta_attr_name
      FROM r_objt_metamap mm
      JOIN r_meta_main m ON mm.meta_id = m.meta_id
     WHERE mm.object_id IN (SELECT data_id FROM objs
                            UNION
                            SELECT coll_id FROM r_coll_main WHERE parent_coll_name = $1)
),
folders AS (
    SELECT 'collection'                           AS type,
           ca.meta_attr_value                     AS uuid,
           c.coll_name                            AS full_path,
           REGEXP_REPLACE(c.coll_name, '.*/', '') AS base_name,
           NULL                                   AS info_type,
           0                                      AS data_size,
           c.create_ts                            AS create_ts,
           c.modify_ts                            AS modify_ts,
           MAX(a.access_type_id)                  AS access_type_id,
           NULL                                   AS data_checksum
      FROM r_coll_main c
      JOIN avus ca ON ca.object_id = c.coll_id
      JOIN r_objt_access a ON c.coll_id = a.object_id
     WHERE c.parent_coll_name = $1
       -- Soft links are catalogued as collections but are not folders the DE shows.
       AND c.coll_type != 'linkPoint'
       AND ca.meta_attr_name = 'ipc_UUID'
       AND a.user_id IN (SELECT group_user_id FROM groups)
     GROUP BY type, uuid, full_path, base_name, info_type, data_size,
              c.create_ts, c.modify_ts, data_checksum
),
files AS (
    SELECT 'dataobject'          AS type,
           m.meta_attr_value     AS uuid,
           $1 || '/' || d.data_name AS full_path,
           d.data_name           AS base_name,
           f.meta_attr_value     AS info_type,
           d.data_size           AS data_size,
           d.create_ts           AS create_ts,
           d.modify_ts           AS modify_ts,
           MAX(a.access_type_id) AS access_type_id,
           d.data_checksum       AS data_checksum
      FROM objs d
      JOIN avus m ON d.data_id = m.object_id
      JOIN r_objt_access a ON d.data_id = a.object_id
      LEFT JOIN (SELECT * FROM avus WHERE meta_attr_name = $9) f ON d.data_id = f.object_id
     WHERE a.user_id IN (SELECT group_user_id FROM groups)
       AND m.meta_attr_name = 'ipc_UUID'
       AND ($5::text[] IS NULL
            OR lower(COALESCE(f.meta_attr_value, '')) = ANY($5::text[])
            OR ($6::boolean AND COALESCE(f.meta_attr_value, '') = ''))
     GROUP BY type, uuid, full_path, base_name, info_type, data_size,
              d.create_ts, d.modify_ts, data_checksum
)
SELECT t.*, COUNT(*) OVER () AS total_count
  FROM (SELECT * FROM folders UNION SELECT * FROM files) AS t
 ORDER BY type ASC, %s %s
 LIMIT $7 OFFSET $8
