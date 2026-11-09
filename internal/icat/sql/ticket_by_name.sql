-- $1 ticket string. A ticket whose object no longer exists is not returned, as an iRODS
-- ticket listing would not return it either.
SELECT t.ticket_id,
       t.ticket_string,
       t.ticket_type,
       u.user_name AS owner,
       COALESCE(c.coll_name, dc.coll_name || '/' || d.data_name) AS path,
       t.ticket_expiry_ts,
       COALESCE(t.uses_limit, 0) AS uses_limit,
       COALESCE(t.uses_count, 0) AS uses_count,
       COALESCE(t.write_file_limit, 0) AS write_file_limit,
       COALESCE(t.write_file_count, 0) AS write_file_count,
       COALESCE(t.write_byte_limit, 0) AS write_byte_limit,
       COALESCE(t.write_byte_count, 0) AS write_byte_count
  FROM r_ticket_main t
  JOIN r_user_main u ON u.user_id = t.user_id
  LEFT JOIN r_coll_main c ON t.object_type = 'collection' AND c.coll_id = t.object_id
  LEFT JOIN r_data_main d ON t.object_type = 'data' AND d.data_id = t.object_id
  LEFT JOIN r_coll_main dc ON dc.coll_id = d.coll_id
 WHERE t.ticket_string = $1
   AND COALESCE(c.coll_name, dc.coll_name || '/' || d.data_name) IS NOT NULL
 -- A data object has a row per replica, all with the same path.
 LIMIT 1
