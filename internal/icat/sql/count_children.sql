-- How many files and subfolders a collection holds, for the file-count and dir-count fields
-- of a directory stat.
--
-- Ported from clj-icat-direct's count-files-in-folder and count-folders-in-folder, which
-- were separate queries. Combining them halves the round trips, and a directory stat always
-- wants both.
--
-- Counts only what the requesting user can see, which is what the separate queries did.
--
-- $1 collection path, $2 username, $3 zone
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
SELECT
    (SELECT count(DISTINCT d.data_id)
       FROM r_data_main d
       JOIN r_coll_main c ON d.coll_id = c.coll_id
       JOIN r_objt_access a ON a.object_id = d.data_id
      WHERE c.coll_name = $1
        AND a.user_id IN (SELECT group_user_id FROM groups)) AS file_count,
    (SELECT count(DISTINCT c.coll_id)
       FROM r_coll_main c
       JOIN r_objt_access a ON a.object_id = c.coll_id
      WHERE c.parent_coll_name = $1
        -- Soft links are catalogued as collections but are not folders the DE shows.
        AND c.coll_type != 'linkPoint'
        AND a.user_id IN (SELECT group_user_id FROM groups)) AS dir_count
