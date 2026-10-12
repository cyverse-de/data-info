-- A sorted, paged listing of a collection's immediate children.
--
-- Ported from clj-icat-direct's mk-paged-folder, which ran this as four statements in a
-- transaction: two temporary tables with an ANALYZE each, then the listing. The temporary
-- tables existed to stop PostgreSQL mis-estimating a CTE's cardinality and choosing nested
-- loops over the catalog. That still happens: a CTE of the children's AVUs was estimated at
-- one row and scanned once per object, about 1.1s of a 1.2s page over 5,000 objects. So the
-- AVUs are joined directly, with the attribute in the join condition, where the per-object
-- lookups use the metamap's object_id index (76ms for the same page). If a plan regresses,
-- the temporary tables are the fallback.
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
-- $10 entity type: any, file or folder
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
      JOIN r_objt_metamap cmm ON cmm.object_id = c.coll_id
      JOIN r_meta_main ca ON ca.meta_id = cmm.meta_id AND ca.meta_attr_name = 'ipc_UUID'
      JOIN r_objt_access a ON c.coll_id = a.object_id
     WHERE $10 IN ('any', 'folder')
       AND c.parent_coll_name = $1
       -- Soft links are catalogued as collections but are not folders the DE shows.
       AND c.coll_type != 'linkPoint'
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
      JOIN r_objt_metamap mm ON mm.object_id = d.data_id
      JOIN r_meta_main m ON m.meta_id = mm.meta_id AND m.meta_attr_name = 'ipc_UUID'
      JOIN r_objt_access a ON d.data_id = a.object_id
      LEFT JOIN (r_objt_metamap fmm
                 JOIN r_meta_main f ON f.meta_id = fmm.meta_id AND f.meta_attr_name = $9)
             ON fmm.object_id = d.data_id
     WHERE $10 IN ('any', 'file')
       AND a.user_id IN (SELECT group_user_id FROM groups)
       -- $6 says whether objects with no info type are kept. It is set independently of
       -- $5, because asking for only untyped objects is a real request: the reference
       -- spells it by naming "unknown" in the info-type list, and it must not be read as
       -- "no filter at all".
       AND (($5::text[] IS NULL AND NOT $6::boolean)
            OR ($5::text[] IS NOT NULL AND lower(COALESCE(f.meta_attr_value, '')) = ANY($5::text[]))
            OR ($6::boolean AND COALESCE(f.meta_attr_value, '') = ''))
     GROUP BY type, uuid, full_path, base_name, info_type, data_size,
              d.create_ts, d.modify_ts, data_checksum
)
SELECT t.*, COUNT(*) OVER () AS total_count
  FROM (SELECT * FROM folders UNION SELECT * FROM files) AS t
 ORDER BY type ASC, %s %s
 -- A null limit means every row, which the path-list endpoint needs: it walks a whole tree
 -- and a page boundary there would silently truncate the list it produces.
 LIMIT $7 OFFSET $8
