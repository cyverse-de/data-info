-- Every user's access to each of a set of absolute paths, in one query.
--
-- Ported from clj-icat-direct's mk-perms-for-item, which answered a single path through a
-- honeysql-built object-lookup CTE.
--
-- $1 dirnames, $2 basenames -- positionally paired
WITH targets AS (
    SELECT dirname, basename
      FROM unnest($1::text[], $2::text[]) AS t(dirname, basename)
),
objs AS (
    SELECT c.coll_id AS object_id, c.coll_name AS full_path
      FROM r_coll_main c
      JOIN targets t ON c.coll_name = t.dirname || '/' || t.basename
    UNION
    SELECT d.data_id, c.coll_name || '/' || d.data_name
      FROM r_data_main d
      JOIN r_coll_main c ON d.coll_id = c.coll_id
      JOIN targets t ON c.coll_name = t.dirname AND d.data_name = t.basename
     WHERE d.data_repl_num = (SELECT MIN(d2.data_repl_num)
                                FROM r_data_main d2
                               WHERE d2.data_id = d.data_id)
)
-- No DISTINCT, matching the Clojure query. What stops an object appearing twice here is
-- the replica filter above: r_data_main holds a row per replica, and the Clojure object
-- lookup does not exclude the extras. r_objt_access is keyed by object and user, so a user
-- cannot hold two access rows on one object. Whether share counts deduplicate is a decision
-- for the caller; see docs/deferred-fixes.md.
SELECT objs.object_id,
       objs.full_path,
       u.user_name,
       u.zone_name,
       a.access_type_id
  FROM objs
  JOIN r_objt_access a ON a.object_id = objs.object_id
  JOIN r_user_main u ON u.user_id = a.user_id
 ORDER BY objs.full_path, u.user_name, a.access_type_id
