-- Native agent sessions (session_start, sent by the SessionStart hook) and the
-- environment descriptors every event now carries.
alter table public.cli_events
  add column account text,
  add column plan text,
  add column install_channel text,
  add column timezone text,
  add column locale text;

alter table public.cli_events drop constraint cli_events_event_check;
alter table public.cli_events add constraint cli_events_event_check check (event in (
  'command_run', 'consent_granted', 'runtime_bootstrap',
  'agent_tool_call', 'first_run', 'engine_session', 'session_start'
));

-- IP retention: clear addresses older than 90 days. Every other field stays,
-- so users/retention/churn keep working on anonymous_id.
select cron.schedule(
  'cli-events-ip-retention',
  '17 3 * * *',
  $$update public.cli_events set ip = null where ip is not null and received_at < now() - interval '90 days'$$
);

create or replace view analytics.users with (security_invoker = true) as
select
  anonymous_id,
  min(received_at) as first_seen,
  max(received_at) as last_seen,
  count(distinct (received_at at time zone 'utc')::date) as active_days,
  count(*) as events,
  count(*) filter (where event = 'command_run') as commands,
  count(*) filter (where exit_class = 'error') as errors,
  (array_agg(cli_version order by received_at desc) filter (where cli_version is not null))[1] as cli_version,
  (array_agg(os order by received_at desc))[1] as os,
  (array_agg(arch order by received_at desc))[1] as arch,
  (array_agg(ip order by received_at desc) filter (where ip is not null))[1] as last_ip,
  count(distinct ip) as distinct_ips,
  array_agg(distinct agent) filter (where agent is not null) as agents,
  coalesce(sum(tokens_processed), 0) as tokens_processed,
  coalesce(sum(tokens_saved), 0) as tokens_saved,
  bool_or(logged_in) as logged_in_at_first_run,
  case
    when max(received_at) >= now() - interval '7 days' then 'active'
    when max(received_at) >= now() - interval '30 days' then 'at_risk'
    else 'churned'
  end as status,
  count(*) filter (where event = 'session_start') as sessions,
  (array_agg(account order by received_at desc) filter (where account is not null))[1] as account,
  (array_agg(plan order by received_at desc) filter (where plan is not null))[1] as plan,
  (array_agg(install_channel order by received_at desc) filter (where install_channel is not null))[1] as install_channel,
  (array_agg(timezone order by received_at desc) filter (where timezone is not null))[1] as timezone,
  (array_agg(locale order by received_at desc) filter (where locale is not null))[1] as locale
from public.cli_events
group by anonymous_id;

create or replace view analytics.summary with (security_invoker = true) as
select
  count(*) as users_total,
  count(*) filter (where last_seen >= now() - interval '1 day') as active_1d,
  count(*) filter (where last_seen >= now() - interval '7 days') as active_7d,
  count(*) filter (where last_seen >= now() - interval '30 days') as active_30d,
  count(*) filter (where first_seen >= now() - interval '7 days') as new_7d,
  count(*) filter (where first_seen >= now() - interval '30 days') as new_30d,
  count(*) filter (where status = 'churned') as churned_30d_plus,
  sum(tokens_processed) as tokens_processed_total,
  sum(tokens_saved) as tokens_saved_total,
  (select coalesce(sum(tokens_saved), 0) from public.cli_events where received_at >= now() - interval '30 days') as tokens_saved_30d,
  (select count(distinct ip) from public.cli_events) as distinct_ips_total,
  (select count(*) from public.cli_events where event = 'session_start' and received_at >= now() - interval '30 days') as sessions_30d,
  count(*) filter (where account = 'connected') as users_with_account
from analytics.users;

create or replace view analytics.daily with (security_invoker = true) as
with activity as (
  select
    (received_at at time zone 'utc')::date as day,
    anonymous_id,
    count(*) as events,
    count(*) filter (where exit_class = 'error') as errors,
    coalesce(sum(tokens_processed), 0) as tokens_processed,
    coalesce(sum(tokens_saved), 0) as tokens_saved,
    count(*) filter (where event = 'session_start') as sessions
  from public.cli_events
  group by 1, 2
),
first_day as (select anonymous_id, min(day) as day from activity group by 1),
days as (
  select g::date as day
  from (select min(day) as lo from activity) b,
       generate_series(b.lo, (now() at time zone 'utc')::date, interval '1 day') g
)
select
  d.day,
  count(distinct a.anonymous_id) filter (where a.day = d.day) as dau,
  count(distinct a.anonymous_id) filter (where a.day > d.day - 7) as wau,
  count(distinct a.anonymous_id) as mau,
  (select count(*) from first_day f where f.day = d.day) as new_users,
  coalesce(sum(a.events) filter (where a.day = d.day), 0) as events,
  coalesce(sum(a.errors) filter (where a.day = d.day), 0) as errors,
  coalesce(sum(a.tokens_processed) filter (where a.day = d.day), 0) as tokens_processed,
  coalesce(sum(a.tokens_saved) filter (where a.day = d.day), 0) as tokens_saved,
  coalesce(sum(a.sessions) filter (where a.day = d.day), 0) as sessions
from days d
left join activity a on a.day > d.day - 30 and a.day <= d.day
group by d.day;

create view analytics.agent_usage with (security_invoker = true) as
select
  agent,
  count(*) as sessions,
  count(distinct anonymous_id) as users,
  count(*) filter (where received_at >= now() - interval '30 days') as sessions_30d,
  count(distinct anonymous_id) filter (where received_at >= now() - interval '30 days') as users_30d,
  coalesce(sum(tokens_saved), 0) as tokens_saved,
  max(received_at) as last_session
from public.cli_events
where event = 'session_start'
group by agent;
