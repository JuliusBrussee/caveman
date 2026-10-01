-- CLI usage telemetry. Written only by the cli-telemetry Edge Function
-- (service role); anon/authenticated have no access.
create table public.cli_events (
  id bigint generated always as identity primary key,
  received_at timestamptz not null default now(),
  -- Client IP as seen by the edge (cf-connecting-ip), not client-supplied.
  ip inet,
  ts timestamptz not null,
  anonymous_id uuid not null,
  event text not null check (event in (
    'command_run', 'consent_granted', 'runtime_bootstrap',
    'agent_tool_call', 'first_run', 'engine_session'
  )),
  command text,
  subcommand text,
  agent text,
  cli_version text,
  os text not null,
  arch text not null,
  node_major smallint,
  duration_ms bigint,
  exit_class text,
  error_class text,
  -- command_run: local proxy token delta since the previous event
  tokens_processed bigint,
  tokens_saved bigint,
  tokens_basis text,
  -- first_run: 30-day retrospective scan aggregates
  sessions_total bigint,
  sessions_scanned bigint,
  tokens_observed bigint,
  would_cut_tokens bigint,
  would_cut_stream_tokens bigint,
  scan_ok boolean,
  engine_used boolean,
  time_boxed boolean,
  logged_in boolean,
  -- engine_session: per-wrap local proxy aggregates
  measurement_mode text,
  measurement_ok boolean,
  requests_observed bigint,
  input_tokens_observed bigint,
  compression_eligible_requests bigint,
  compression_eligibility_known boolean,
  compression_tokens_before bigint,
  compression_tokens_after bigint,
  compression_tokens_saved bigint,
  compression_pair_known boolean,
  estimated_cut_tokens bigint,
  cache_read_tokens bigint,
  cache_write_tokens bigint,
  cache_bust_requests bigint,
  headline_suppressed boolean
);

comment on table public.cli_events is
  'CLI telemetry events. RLS on with no policies on purpose: only the service role (cli-telemetry Edge Function) writes.';

create index cli_events_received_at_idx on public.cli_events (received_at);

alter table public.cli_events enable row level security;

-- This project still has the old default grants (anon/authenticated get ALL on
-- new public tables), so strip them explicitly. The service role needs SELECT
-- only for the rate-limit trigger's lookback.
revoke all on table public.cli_events from anon, authenticated, service_role;
revoke all on sequence public.cli_events_id_seq from anon, authenticated, service_role;
grant select, insert on table public.cli_events to service_role;

-- Per-client rate limit, enforced for every writer. Rows over the limit are
-- skipped (BEFORE trigger returns NULL) instead of failing the batch; IPv6
-- clients are bucketed by /64 since one host can own the whole prefix.
create schema if not exists private;

create function private.cli_events_rate_limit()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  -- ponytail: scans the last minute via the received_at index; add an
  -- (ip, received_at) index if ingest volume makes this slow.
  if new.ip is not null and (
    select count(*)
    from public.cli_events e
    where e.received_at > now() - interval '1 minute'
      and e.ip <<= network(set_masklen(new.ip, case family(new.ip) when 6 then 64 else 32 end))
  ) >= 120 then
    return null;
  end if;
  return new;
end;
$$;

create trigger cli_events_rate_limit
before insert on public.cli_events
for each row execute function private.cli_events_rate_limit();
