-- A sorted, paged listing of the catalog items carrying a given set of data ids.
--
-- Ported from clj-icat-direct's :paged-uuid-listing, which interpolated the id list and the
-- info-type condition into the statement as quoted text. It is the same shape as
-- paged_folder.sql -- collections and data objects selected separately, unioned, sorted
-- together and paged -- over a set of ids rather than a parent collection. That union is
-- why this cannot be a GenQuery: the protocol cannot select two object types at once, pivot
-- an AVU into the row, or take a MAX over the requesting user's groups.
--
-- Unlike paged_folder.sql this reports no total. The count is its own query, because the
-- window function would have to run over the union on every page.
--
-- $1 data ids
-- $2 username, $3 zone
-- $4 group ids, or NULL to derive them from $2 and $3
-- $5 info types to keep, or NULL for all
-- $6 whether an object with no info type is kept when $5 is set
-- $7 limit, $8 offset
-- $9 the AVU attribute holding an object's info type
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
uuids AS (
    SELECT m.meta_attr_value AS uuid,
           o.object_id       AS object_id
      FROM r_meta_main m
      JOIN r_objt_metamap o ON m.meta_id = o.meta_id
     WHERE m.meta_attr_name = 'ipc_UUID'
       AND m.meta_attr_value = ANY($1::text[])
       AND o.object_id IN (SELECT object_id
                             FROM r_objt_access
                            WHERE user_id IN (SELECT group_user_id FROM groups))
),
file_types AS (
    SELECT om.object_id, mm.meta_attr_value
      FROM r_objt_metamap om
      JOIN r_meta_main mm ON mm.meta_id = om.meta_id
     WHERE om.object_id IN (SELECT object_id FROM uuids)
       AND mm.meta_attr_name = $9
)
SELECT p.type,
       p.uuid,
       p.full_path,
       p.base_name,
       p.info_type,
       p.data_size,
       p.create_ts,
       p.modify_ts,
       MAX(p.access_type_id) AS access_type_id,
       p.data_checksum
  FROM (SELECT 'collection'                           AS type,
               u.uuid                                 AS uuid,
               c.coll_name                            AS full_path,
               REGEXP_REPLACE(c.coll_name, '.*/', '') AS base_name,
               NULL                                   AS info_type,
               0                                      AS data_size,
               c.create_ts                            AS create_ts,
               c.modify_ts                            AS modify_ts,
               a.access_type_id                       AS access_type_id,
               NULL                                   AS data_checksum
          FROM uuids u
          JOIN r_coll_main c ON u.object_id = c.coll_id
          JOIN r_objt_access a ON c.coll_id = a.object_id
          -- Soft links are catalogued as collections but are not folders the DE shows.
         WHERE c.coll_type != 'linkPoint'
           AND a.user_id IN (SELECT group_user_id FROM groups)
        UNION
        SELECT 'dataobject'                         AS type,
               u.uuid                               AS uuid,
               (c.coll_name || '/' || d1.data_name) AS full_path,
               d1.data_name                         AS base_name,
               f.meta_attr_value                    AS info_type,
               d1.data_size                         AS data_size,
               d1.create_ts                         AS create_ts,
               d1.modify_ts                         AS modify_ts,
               a.access_type_id                     AS access_type_id,
               d1.data_checksum                     AS data_checksum
          FROM uuids u
          JOIN r_data_main d1 ON u.object_id = d1.data_id
          JOIN r_coll_main c ON d1.coll_id = c.coll_id
          JOIN r_objt_access a ON d1.data_id = a.object_id
          LEFT JOIN file_types f ON d1.data_id = f.object_id
          -- One row per data object rather than per replica.
         WHERE d1.data_repl_num = (SELECT MIN(d2.data_repl_num)
                                     FROM r_data_main d2
                                    WHERE d2.data_id = d1.data_id)
           AND a.user_id IN (SELECT group_user_id FROM groups)
           -- $6 says whether objects with no info type are kept. It is set independently of
           -- $5, because asking for only untyped objects is a real request: the reference
           -- spells it by naming "unknown" in the info-type list, and it must not be read
           -- as "no filter at all".
           AND (($5::text[] IS NULL AND NOT $6::boolean)
                OR ($5::text[] IS NOT NULL AND lower(COALESCE(f.meta_attr_value, '')) = ANY($5::text[]))
                OR ($6::boolean AND COALESCE(f.meta_attr_value, '') = ''))) AS p
 GROUP BY p.type, p.uuid, p.full_path, p.base_name, p.info_type, p.data_size, p.create_ts,
          p.modify_ts, p.data_checksum
 ORDER BY p.type ASC, %s %s
 LIMIT $7 OFFSET $8
