-- Replace the last-minute lookback rate limit with per-minute counters. The
-- lookback scanned every sender's rows for each inserted row, missed rows from
-- concurrent uncommitted batches, and let rows without an IP through. A counter
-- row is O(1) per insert, and its row lock serializes concurrent batches from
-- the same sender.
create table private.cli_events_rate (
  bucket cidr not null,
  minute timestamptz not null,
  n integer not null,
  primary key (bucket, minute)
);

create index cli_events_rate_minute_idx on private.cli_events_rate (minute);

-- private is not exposed through the Data API; RLS is defense in depth.
alter table private.cli_events_rate enable row level security;
revoke all on table private.cli_events_rate from anon, authenticated;

-- The trigger runs as the inserting role (service role via the Edge Function).
-- service_role keeps SELECT on public.cli_events for reporting queries.
grant usage on schema private to service_role;
grant select, insert, update on table private.cli_events_rate to service_role;

create or replace function private.cli_events_rate_limit()
returns trigger
language plpgsql
set search_path = ''
as $$
declare
  v_minute timestamptz := date_trunc('minute', now());
  -- IPv4 per address, IPv6 per /56 (a typical single-customer delegation);
  -- rows without an IP share one bucket so they cannot bypass the limit.
  v_bucket cidr := coalesce(
    network(set_masklen(new.ip, case family(new.ip) when 6 then 56 else 32 end)),
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

create extension if not exists pg_cron with schema pg_catalog;
grant usage on schema cron to postgres;
grant all privileges on all tables in schema cron to postgres;

select cron.schedule(
  'cli-events-rate-prune',
  '*/10 * * * *',
  $$delete from private.cli_events_rate where minute < now() - interval '10 minutes'$$
);
