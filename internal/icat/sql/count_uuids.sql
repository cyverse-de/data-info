-- How many of a set of data ids name something the user can see: every collection, plus the
-- data objects whose info type passes the filter.
--
-- Ported from clj-icat-direct's mk-count-uuids-of-file-type. It is the total that goes
-- alongside a page from paged_uuid_listing.sql, so the two must agree about what counts --
-- which is why the id, access and info-type conditions are written the same way in both.
--
-- $1 data ids
-- $2 username, $3 zone
-- $4 group ids, or NULL to derive them from $2 and $3
-- $5 info types to keep, or NULL for all
-- $6 whether an object with no info type is kept when $5 is set
-- $7 the AVU attribute holding an object's info type
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
       AND mm.meta_attr_name = $7
)
SELECT ((SELECT COUNT(*)
           FROM uuids
          WHERE object_id IN (SELECT coll_id
                                FROM r_coll_main
                               WHERE coll_type != 'linkPoint'))
        +
        (SELECT COUNT(*)
           FROM uuids
          WHERE object_id IN (SELECT d.data_id
                                FROM r_data_main d
                                LEFT JOIN file_types f ON d.data_id = f.object_id
                               WHERE (($5::text[] IS NULL AND NOT $6::boolean)
                                      OR ($5::text[] IS NOT NULL
                                          AND lower(COALESCE(f.meta_attr_value, '')) = ANY($5::text[]))
                                      OR ($6::boolean AND COALESCE(f.meta_attr_value, '') = ''))))) AS total
