-- The group ids a user belongs to, which is the set every permission-filtered query is
-- scoped by. iRODS records a user's own id as a group containing only themselves, so this
-- returns that too and no caller has to add it.
--
-- Ported from clj-icat-direct's mk-groups, which interpolated the username and zone
-- straight into the statement.
SELECT g.group_user_id
  FROM r_user_group g
 WHERE g.user_id IN (SELECT u.user_id
                       FROM r_user_main u
                      WHERE u.user_name = $1
                        AND u.zone_name = $2)
