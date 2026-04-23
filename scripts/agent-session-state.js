#!/usr/bin/env node

const fs = require('node:fs');
const path = require('node:path');

function resolveSessionSchemaModule() {
  const candidates = [
    path.resolve(__dirname, '..', 'vscode', 'guardex-active-agents', 'session-schema.js'),
    path.resolve(__dirname, '..', 'templates', 'vscode', 'guardex-active-agents', 'session-schema.js'),
  ];

  for (const candidate of candidates) {
    if (fs.existsSync(candidate)) {
      return require(candidate);
    }
  }

  throw new Error('Could not resolve Guardex active-agent session schema module.');
}

const sessionSchema = resolveSessionSchemaModule();

function usage() {
  return (
    'Usage:\n' +
    '  node scripts/agent-session-state.js start --repo <path> --branch <name> --task <task> --agent <agent> --worktree <path> --pid <pid> --cli <name> [--task-mode <caveman|omx>] [--openspec-tier <T0|T1|T2|T3>] [--routing-reason <text>] [--state <working|thinking|idle>]\n' +
    '  node scripts/agent-session-state.js heartbeat --repo <path> --branch <name> [--state <working|thinking|idle>]\n' +
    '  node scripts/agent-session-state.js terminate --repo <path> --branch <name>\n' +
    '  node scripts/agent-session-state.js stop --repo <path> --branch <name>\n'
  );
}

function parseOptions(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const token = argv[index];
    if (!token.startsWith('--')) {
      throw new Error(`Unexpected argument: ${token}`);
    }
    const key = token.slice(2);
    const value = argv[index + 1];
    if (!value || value.startsWith('--')) {
      throw new Error(`Missing value for --${key}`);
    }
    options[key] = value;
    index += 1;
  }
  return options;
}

function requireOption(options, key) {
  const value = options[key];
  if (!value) {
    throw new Error(`Missing required option --${key}`);
  }
  return value;
}

function writeSessionRecord(options) {
  const repoRoot = requireOption(options, 'repo');
  const branch = requireOption(options, 'branch');
  const record = sessionSchema.buildSessionRecord({
    repoRoot,
    branch,
    taskName: requireOption(options, 'task'),
    agentName: requireOption(options, 'agent'),
    worktreePath: requireOption(options, 'worktree'),
    pid: requireOption(options, 'pid'),
    cliName: requireOption(options, 'cli'),
    taskMode: options['task-mode'],
    openspecTier: options['openspec-tier'],
    taskRoutingReason: options['routing-reason'],
    state: options.state,
  });

  const targetPath = sessionSchema.sessionFilePathForBranch(repoRoot, branch);
  fs.mkdirSync(path.dirname(targetPath), { recursive: true });
  fs.writeFileSync(targetPath, `${JSON.stringify(record, null, 2)}\n`, 'utf8');
}

function refreshSessionRecord(options) {
  const repoRoot = requireOption(options, 'repo');
  const branch = requireOption(options, 'branch');
  const targetPath = sessionSchema.sessionFilePathForBranch(repoRoot, branch);
  if (!fs.existsSync(targetPath)) {
    return;
  }

  const parsed = JSON.parse(fs.readFileSync(targetPath, 'utf8'));
  const nextRecord = {
    ...parsed,
    lastHeartbeatAt: new Date().toISOString(),
  };
  if (options.state) {
    nextRecord.state = options.state;
  }

  fs.writeFileSync(targetPath, `${JSON.stringify(nextRecord, null, 2)}\n`, 'utf8');
}

function readSessionRecord(options) {
  const repoRoot = requireOption(options, 'repo');
  const branch = requireOption(options, 'branch');
  const targetPath = sessionSchema.sessionFilePathForBranch(repoRoot, branch);
  if (!fs.existsSync(targetPath)) {
    return null;
  }
  return JSON.parse(fs.readFileSync(targetPath, 'utf8'));
}

function terminateSessionProcess(options) {
  const record = readSessionRecord(options);
  const pid = Number(record?.pid);
  if (!Number.isInteger(pid) || pid <= 0) {
    throw new Error('No live pid recorded for branch.');
  }

  try {
    process.kill(pid, 'SIGTERM');
  } catch (error) {
    if (error?.code === 'ESRCH') {
      return;
    }
    throw error;
  }
}

function removeSessionRecord(options) {
  const repoRoot = requireOption(options, 'repo');
  const branch = requireOption(options, 'branch');
  const targetPath = sessionSchema.sessionFilePathForBranch(repoRoot, branch);
  if (fs.existsSync(targetPath)) {
    fs.unlinkSync(targetPath);
  }
}

function main() {
  const [command, ...rest] = process.argv.slice(2);
  if (!command || ['-h', '--help', 'help'].includes(command)) {
    process.stdout.write(usage());
    return;
  }

  const options = parseOptions(rest);
  if (command === 'start') {
    writeSessionRecord(options);
    return;
  }
  if (command === 'heartbeat') {
    refreshSessionRecord(options);
    return;
  }
  if (command === 'terminate') {
    terminateSessionProcess(options);
    return;
  }
  if (command === 'stop') {
    removeSessionRecord(options);
    return;
  }

  throw new Error(`Unknown subcommand: ${command}`);
}

try {
  main();
} catch (error) {
  process.stderr.write(`[guardex-active-session] ${error.message}\n`);
  process.stderr.write(usage());
  process.exitCode = 1;
}
