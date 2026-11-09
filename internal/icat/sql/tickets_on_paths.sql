-- $1 full paths, $2 dirnames, $3 basenames (positionally paired), $4 whether tickets on
-- anything inside the paths count too.
--
-- r_ticket_main has no index on object_id, so it is scanned once; everything else is
-- reached through indexes, starting from the requested paths rather than from the tickets,
-- so the cost follows the size of the requested trees and not the size of the catalog.
WITH RECURSIVE colls AS (
    SELECT c.coll_id, c.coll_name
      FROM r_coll_main c
     WHERE c.coll_name = ANY($1::text[])
    -- UNION rather than UNION ALL: the root collection is its own parent.
    UNION
    SELECT c.coll_id, c.coll_name
      FROM r_coll_main c
      JOIN colls p ON c.parent_coll_name = p.coll_name
     WHERE $4
),
objects AS (
    SELECT coll_id AS object_id, 'collection' AS object_type, coll_name AS path
      FROM colls
    UNION
    SELECT d.data_id, 'data', c.coll_name || '/' || d.data_name
      FROM r_data_main d
      JOIN colls c ON d.coll_id = c.coll_id
     WHERE $4
    UNION
    SELECT d.data_id, 'data', c.coll_name || '/' || d.data_name
      FROM unnest($2::text[], $3::text[]) AS t(dirname, basename)
      JOIN r_coll_main c ON c.coll_name = t.dirname
      JOIN r_data_main d ON d.coll_id = c.coll_id AND d.data_name = t.basename
)
SELECT t.ticket_id,
       t.ticket_string,
       t.ticket_type,
       u.user_name AS owner,
       o.path,
       t.ticket_expiry_ts,
       COALESCE(t.uses_limit, 0) AS uses_limit,
       COALESCE(t.uses_count, 0) AS uses_count,
       COALESCE(t.write_file_limit, 0) AS write_file_limit,
       COALESCE(t.write_file_count, 0) AS write_file_count,
       COALESCE(t.write_byte_limit, 0) AS write_byte_limit,
       COALESCE(t.write_byte_count, 0) AS write_byte_count
  FROM r_ticket_main t
  JOIN objects o ON o.object_id = t.object_id AND o.object_type = t.object_type
  JOIN r_user_main u ON u.user_id = t.user_id
 ORDER BY o.path, t.ticket_string
