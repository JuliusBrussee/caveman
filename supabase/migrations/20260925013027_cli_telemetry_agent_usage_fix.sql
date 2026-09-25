-- The token delta on session_start covers all proxy traffic since the previous
-- event, whichever agent produced it, so it cannot be attributed per agent.
-- Per-install and daily token totals (analytics.users / daily / summary) sum
-- tokens_saved across command_run and session_start; each delta is disjoint.
drop view analytics.agent_usage;

create view analytics.agent_usage with (security_invoker = true) as
select
  agent,
  count(*) as sessions,
  count(distinct anonymous_id) as users,
  count(*) filter (where received_at >= now() - interval '30 days') as sessions_30d,
  count(distinct anonymous_id) filter (where received_at >= now() - interval '30 days') as users_30d,
  max(received_at) as last_session
from public.cli_events
where event = 'session_start'
group by agent;
