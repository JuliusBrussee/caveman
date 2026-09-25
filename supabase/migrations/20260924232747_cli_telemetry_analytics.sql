-- Product analytics over public.cli_events. The analytics schema is not
-- exposed through the Data API and grants nothing to anon/authenticated; read
-- it from the SQL editor or a direct Postgres connection.
--
-- A "user" is one CLI install: the persisted anonymous_id in
-- ~/.caveman-cloud/config.json. One person on two machines counts twice; a
-- fresh config (or telemetry off then on) starts a new id. Activity is dated
-- by server received_at (UTC), never the client-claimed ts. Tokens saved come
-- from command_run only: engine_session reports the same proxy savings for a
-- wrap session, so adding both would double count. All token figures are
-- inferred tokenizer estimates, never billed counts.
--
-- ponytail: plain views recompute on every read; switch the heavy ones to
-- materialized views refreshed by pg_cron once the table is large.
create schema if not exists analytics;
revoke all on schema analytics from public, anon, authenticated;

create view analytics.users with (security_invoker = true) as
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
  end as status
from public.cli_events
group by anonymous_id;

create view analytics.summary with (security_invoker = true) as
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
  (select count(distinct ip) from public.cli_events) as distinct_ips_total
from analytics.users;

create view analytics.daily with (security_invoker = true) as
with activity as (
  select
    (received_at at time zone 'utc')::date as day,
    anonymous_id,
    count(*) as events,
    count(*) filter (where exit_class = 'error') as errors,
    coalesce(sum(tokens_processed), 0) as tokens_processed,
    coalesce(sum(tokens_saved), 0) as tokens_saved
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
  coalesce(sum(a.tokens_saved) filter (where a.day = d.day), 0) as tokens_saved
from days d
left join activity a on a.day > d.day - 30 and a.day <= d.day
group by d.day;

create view analytics.retention_weekly with (security_invoker = true) as
with activity as (
  select distinct anonymous_id, date_trunc('week', received_at at time zone 'utc')::date as week
  from public.cli_events
),
cohorts as (select anonymous_id, min(week) as cohort_week from activity group by 1),
sizes as (select cohort_week, count(*) as cohort_size from cohorts group by 1)
select
  c.cohort_week,
  s.cohort_size,
  (a.week - c.cohort_week) / 7 as week_offset,
  count(*) as retained_users,
  round(100.0 * count(*) / s.cohort_size, 1) as retained_pct
from cohorts c
join activity a using (anonymous_id)
join sizes s using (cohort_week)
group by 1, 2, 3;

-- The current week is partial: its churn keeps falling as the week fills in.
create view analytics.churn_weekly with (security_invoker = true) as
with activity as (
  select distinct anonymous_id, date_trunc('week', received_at at time zone 'utc')::date as week
  from public.cli_events
),
firsts as (select anonymous_id, min(week) as first_week from activity group by 1),
weeks as (
  select g::date as week
  from (select min(week) as lo from activity) b,
       generate_series(b.lo, date_trunc('week', now() at time zone 'utc')::date, interval '7 days') g
)
select
  w.week,
  w.week = date_trunc('week', now() at time zone 'utc')::date as partial_week,
  count(cur.anonymous_id) as active_users,
  count(*) filter (where f.first_week = w.week) as new_users,
  count(*) filter (where cur.anonymous_id is not null and f.first_week < w.week and prev.anonymous_id is null) as resurrected,
  count(prev.anonymous_id) as prev_week_active,
  count(*) filter (where prev.anonymous_id is not null and cur.anonymous_id is null) as churned,
  round(100.0 * count(*) filter (where prev.anonymous_id is not null and cur.anonymous_id is null)
    / nullif(count(prev.anonymous_id), 0), 1) as churn_pct
from weeks w
cross join firsts f
left join activity cur on cur.anonymous_id = f.anonymous_id and cur.week = w.week
left join activity prev on prev.anonymous_id = f.anonymous_id and prev.week = w.week - 7
where f.first_week <= w.week
group by w.week;

create view analytics.command_usage with (security_invoker = true) as
select
  command,
  subcommand,
  agent,
  count(*) as runs,
  count(distinct anonymous_id) as users,
  count(*) filter (where exit_class = 'error') as errors,
  round(100.0 * count(*) filter (where exit_class = 'error') / count(*), 1) as error_pct,
  percentile_cont(0.5) within group (order by duration_ms) as p50_duration_ms,
  count(*) filter (where received_at >= now() - interval '30 days') as runs_30d,
  count(distinct anonymous_id) filter (where received_at >= now() - interval '30 days') as users_30d,
  max(received_at) as last_run
from public.cli_events
where event = 'command_run'
group by 1, 2, 3;
