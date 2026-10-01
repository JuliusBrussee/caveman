-- Review follow-ups: no sender can hold the global ceiling for long, a new
-- install is counted only once it is stored, events expire after 13 months,
-- and the Edge Function can ask whether the global ceiling is full.
--
-- Lockout arithmetic. The global ceiling is 10000 rows/min for everyone.
--   Before: 120/min per sender, no daily limit. 84 IPv4 addresses filled the
--   ceiling every minute, all day.
--   Now: 30/min and 5000/day per sender. Filling one minute takes
--   ceil(10000 / 30) = 334 addresses, and each of them is spent for the day
--   after 5000 / 30 = ~167 minutes at full rate. Filling the whole day
--   (1440 * 10000 = 14.4M rows) takes 14.4M / 5000 = 2880 addresses.
-- A person sends one event per interactive command and one per agent session,
-- so 30/min and 5000/day per address still covers an office behind one NAT.
-- ponytail: an IPv6 sender with a /32 has 65536 /48 buckets; add a coarser
-- IPv6 daily bucket if that shows up in cli_events_rate_daily.

create table private.cli_events_rate_daily (
  bucket cidr not null,
  day date not null,
  n integer not null,
  primary key (bucket, day)
);
alter table private.cli_events_rate_daily enable row level security;
revoke all on table private.cli_events_rate_daily from public, anon, authenticated;
grant select, insert, update on table private.cli_events_rate_daily to service_role;

create or replace function private.cli_events_rate_limit()
returns trigger
language plpgsql
set search_path = ''
as $$
declare
  v_minute timestamptz := date_trunc('minute', now());
  v_day date := (now() at time zone 'utc')::date;
  -- IPv4 per address, IPv6 per /48. Rows without an IP share one bucket.
  v_bucket cidr := coalesce(
    network(set_masklen(new.ip, case family(new.ip) when 6 then 48 else 32 end)),
    '0.0.0.0/0'
  );
  v_n integer;
begin
  -- Per-sender limits count attempts, so a flood spends its own budget.
  insert into private.cli_events_rate as r (bucket, minute, n)
  values (v_bucket, v_minute, 1)
  on conflict (bucket, minute) do update set n = r.n + 1
  returning r.n into v_n;
  if v_n > 30 then
    return null;
  end if;

  insert into private.cli_events_rate_daily as r (bucket, day, n)
  values (v_bucket, v_day, 1)
  on conflict (bucket, day) do update set n = r.n + 1
  returning r.n into v_n;
  if v_n > 5000 then
    return null;
  end if;

  -- Global ceiling ('::/0' row) bounds table growth however many senders
  -- there are. Keep the 10000 in step with public.cli_events_global_full().
  -- ponytail: one hot row serializes all ingest; shard it if legitimate
  -- volume approaches the ceiling.
  insert into private.cli_events_rate as r (bucket, minute, n)
  values ('::/0', v_minute, 1)
  on conflict (bucket, minute) do update set n = r.n + 1
  returning r.n into v_n;
  if v_n > 10000 then
    return null;
  end if;

  -- New installs per sender per day, checked last so an event another limit
  -- dropped never spends a slot (its id would look new again next time and
  -- count twice). 500 fits a company-wide rollout behind one NAT in a day; a
  -- forger still gets at most 500 fake "users" per address per day.
  if not exists (select 1 from public.cli_events e where e.anonymous_id = new.anonymous_id) then
    insert into private.cli_events_new_ids as r (bucket, day, n)
    values (v_bucket, v_day, 1)
    on conflict (bucket, day) do update set n = r.n + 1
    returning r.n into v_n;
    if v_n > 500 then
      return null;
    end if;
  end if;

  return new;
end;
$$;

-- Called by the Edge Function (service role) after an insert comes back
-- short: true while this minute's global ceiling is full.
create function public.cli_events_global_full()
returns boolean
language sql
stable
set search_path = ''
as $$
  select coalesce((
    select r.n >= 10000
    from private.cli_events_rate r
    where r.bucket = '::/0' and r.minute = date_trunc('minute', now())
  ), false)
$$;
revoke all on function public.cli_events_global_full() from public, anon, authenticated;
grant execute on function public.cli_events_global_full() to service_role;

select cron.schedule(
  'cli-events-rate-prune',
  '*/10 * * * *',
  $$delete from private.cli_events_rate where minute < now() - interval '10 minutes';
    delete from private.cli_events_rate_daily where day < (now() at time zone 'utc')::date - 1;
    delete from private.cli_events_new_ids where day < (now() at time zone 'utc')::date - 1$$
);

-- Row retention: events older than 13 months are deleted (SECURITY.md). IPs
-- are already cleared at 90 days by cli-events-ip-retention.
select cron.schedule(
  'cli-events-retention',
  '37 3 * * *',
  $$delete from public.cli_events where received_at < now() - interval '13 months'$$
);

-- Deletion request for one install (SECURITY.md), run in the SQL editor:
--   delete from public.cli_events where anonymous_id = '<install id>';
-- cli_events_anonymous_id_idx serves it.
