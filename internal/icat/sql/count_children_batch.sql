-- How many files and subfolders each of a set of collections holds, in one query.
--
-- A directory stat reports both counts, and the bulk endpoints accept a thousand paths, so
-- counting one collection at a time would reintroduce exactly the round-trip-per-path cost
-- the batched item query exists to avoid.
--
-- Counts only what the requesting user can see.
--
-- $1 collection paths, $2 username, $3 zone
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
),
targets AS (
    SELECT unnest($1::text[]) AS coll_name
),
files AS (
    SELECT c.coll_name, count(DISTINCT d.data_id) AS n
      FROM targets t
      JOIN r_coll_main c ON c.coll_name = t.coll_name
      JOIN r_data_main d ON d.coll_id = c.coll_id
      JOIN r_objt_access a ON a.object_id = d.data_id
     WHERE a.user_id IN (SELECT group_user_id FROM groups)
     GROUP BY c.coll_name
),
dirs AS (
    SELECT t.coll_name, count(DISTINCT c.coll_id) AS n
      FROM targets t
      JOIN r_coll_main c ON c.parent_coll_name = t.coll_name
      JOIN r_objt_access a ON a.object_id = c.coll_id
     -- Soft links are catalogued as collections but are not folders the DE shows.
     WHERE c.coll_type != 'linkPoint'
       AND a.user_id IN (SELECT group_user_id FROM groups)
     GROUP BY t.coll_name
)
SELECT t.coll_name           AS full_path,
       COALESCE(files.n, 0)  AS file_count,
       COALESCE(dirs.n, 0)   AS dir_count
  FROM targets t
  LEFT JOIN files ON files.coll_name = t.coll_name
  LEFT JOIN dirs ON dirs.coll_name = t.coll_name
