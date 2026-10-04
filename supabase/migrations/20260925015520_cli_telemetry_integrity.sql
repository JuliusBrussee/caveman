-- Review follow-ups: session_start source, a cap on forged install ids,
-- coarser IPv6 buckets, a cheaper IP-retention scan, and a partial-week flag
-- on retention.
alter table public.cli_events add column session_source text;

-- Serves the new-id lookup below and deletion requests by install id.
create index cli_events_anonymous_id_idx on public.cli_events (anonymous_id);

-- Only rows still holding an IP: cleared rows leave the index, so the daily
-- retention job never rescans them.
create index cli_events_ip_retention_idx on public.cli_events (received_at) where ip is not null;

-- The endpoint is unauthenticated, so "unique users" are cheap to forge with a
-- fresh random anonymous_id per event. Count never-seen ids per client per day.
create table private.cli_events_new_ids (
  bucket cidr not null,
  day date not null,
  n integer not null,
  primary key (bucket, day)
);
alter table private.cli_events_new_ids enable row level security;
revoke all on table private.cli_events_new_ids from anon, authenticated;
grant select, insert, update on table private.cli_events_new_ids to service_role;

create or replace function private.cli_events_rate_limit()
returns trigger
language plpgsql
set search_path = ''
as $$
declare
  v_minute timestamptz := date_trunc('minute', now());
  -- IPv4 per address, IPv6 per /48 (one site's allocation; /56s inside it are
  -- free to rotate). Rows without an IP share one bucket.
  v_bucket cidr := coalesce(
    network(set_masklen(new.ip, case family(new.ip) when 6 then 48 else 32 end)),
    '0.0.0.0/0'
  );
  v_n integer;
begin
  insert into private.cli_events_rate as r (bucket, minute, n)
  values (v_bucket, v_minute, 1)
  on conflict (bucket, minute) do update set n = r.n + 1
  returning r.n into v_n;
  if v_n > 120 then
    return null;
  end if;

  -- ponytail: 50 new installs per client per day; a large shared NAT
  -- (university, conference wifi) can hit it. Raise it if real installs drop.
  if not exists (select 1 from public.cli_events e where e.anonymous_id = new.anonymous_id) then
    insert into private.cli_events_new_ids as r (bucket, day, n)
    values (v_bucket, (now() at time zone 'utc')::date, 1)
    on conflict (bucket, day) do update set n = r.n + 1
    returning r.n into v_n;
    if v_n > 50 then
      return null;
    end if;
  end if;

  -- Global ceiling ('::/0' row) bounds table growth however many senders
  -- there are. ponytail: one hot row serializes all ingest; shard it if
  -- legitimate volume approaches the ceiling.
  insert into private.cli_events_rate as r (bucket, minute, n)
  values ('::/0', v_minute, 1)
  on conflict (bucket, minute) do update set n = r.n + 1
  returning r.n into v_n;
  if v_n > 10000 then
    return null;
  end if;

  return new;
end;
$$;

select cron.schedule(
  'cli-events-rate-prune',
  '*/10 * * * *',
  $$delete from private.cli_events_rate where minute < now() - interval '10 minutes';
    delete from private.cli_events_new_ids where day < (now() at time zone 'utc')::date - 1$$
);

-- Offsets with zero retained users have no row (read a missing offset as 0).
create or replace view analytics.retention_weekly with (security_invoker = true) as
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
  round(100.0 * count(*) / s.cohort_size, 1) as retained_pct,
  bool_or(a.week = date_trunc('week', now() at time zone 'utc')::date) as partial_week
from cohorts c
join activity a using (anonymous_id)
join sizes s using (cohort_week)
group by 1, 2, 3;
