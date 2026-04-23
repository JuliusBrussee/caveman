const fs = require('node:fs');
const path = require('node:path');
const cp = require('node:child_process');

const ACTIVE_SESSIONS_RELATIVE_DIR = path.join('.omx', 'state', 'active-sessions');
const SESSION_SCHEMA_VERSION = 1;
const LOCK_FILE_RELATIVE = path.join('.omx', 'state', 'agent-file-locks.json');
const LOGS_RELATIVE_DIR = path.join('.omx', 'logs');
const AGENT_WORKTREE_LOCK_FILE = 'AGENT.lock';
const MANAGED_WORKTREE_ROOTS = [
  path.join('.omx', 'agent-worktrees'),
  path.join('.omc', 'agent-worktrees'),
];
const MAX_CHANGED_PATH_PREVIEW = 3;
const ACTIVE_SESSIONS_FILTER_PREFIX = ACTIVE_SESSIONS_RELATIVE_DIR.split(path.sep).join('/');
const LOCK_FILE_FILTER_PATH = LOCK_FILE_RELATIVE.split(path.sep).join('/');
const MANAGED_WORKTREE_FILTER_PREFIXES = MANAGED_WORKTREE_ROOTS
  .map((relativeRoot) => relativeRoot.split(path.sep).join('/').replace(/\/+$/, ''));
const IDLE_ACTIVITY_WINDOW_MS = 2 * 60 * 1000;
const STALLED_ACTIVITY_WINDOW_MS = 15 * 60 * 1000;
const HEARTBEAT_STALE_MS = 5 * 60 * 1000;
const DEFAULT_BASE_BRANCH = 'dev';
const DEFAULT_LOG_TAIL_LINE_COUNT = 200;
const ADVISORY_SESSION_STATES = new Set(['working', 'thinking', 'idle']);
const WORKTREE_ACTIVITY_CACHE_TTL_MS = 3_000;
const MAX_WORKTREE_ACTIVITY_STAT_PATHS = 200;
const WORKTREE_ACTIVITY_SKIP_PREFIXES = [
  '.git/',
  '.omx/',
  '.omc/',
  'node_modules/',
  'dist/',
  'build/',
  'coverage/',
  '.next/',
  'out/',
  'vendor/',
];
const WORKTREE_ACTIVITY_PRIORITY_PREFIXES = [
  'src/',
  'app/',
  'apps/',
  'lib/',
  'packages/',
  'scripts/',
  'test/',
  'tests/',
  'vscode/',
  'templates/',
  'openspec/',
  'docs/',
];
const BLOCKING_GIT_STATES = [
  {
    label: 'Rebase in progress.',
    markers: ['REBASE_HEAD', 'rebase-apply', 'rebase-merge'],
  },
  {
    label: 'Merge in progress.',
    markers: ['MERGE_HEAD'],
  },
  {
    label: 'Cherry-pick in progress.',
    markers: ['CHERRY_PICK_HEAD'],
  },
];
const worktreeActivityCache = new Map();

function toNonEmptyString(value, fallback = '') {
  const normalized = typeof value === 'string' ? value.trim() : String(value || '').trim();
  return normalized || fallback;
}

function toPositiveInteger(value) {
  const normalized = Number.parseInt(String(value || ''), 10);
  return Number.isInteger(normalized) && normalized > 0 ? normalized : null;
}

function normalizeTaskMode(value) {
  const normalized = toNonEmptyString(value).toLowerCase();
  return normalized === 'caveman' || normalized === 'omx' ? normalized : '';
}

function normalizeOpenSpecTier(value) {
  const normalized = toNonEmptyString(value).toUpperCase();
  return ['T0', 'T1', 'T2', 'T3'].includes(normalized) ? normalized : '';
}

function normalizeAdvisoryState(value, fallback = 'working') {
  const normalized = toNonEmptyString(value).toLowerCase();
  return ADVISORY_SESSION_STATES.has(normalized) ? normalized : fallback;
}

function sanitizeBranchForFile(branch) {
  const normalized = toNonEmptyString(branch, 'session');
  return normalized.replace(/[^a-zA-Z0-9._-]+/g, '__').replace(/^_+|_+$/g, '') || 'session';
}

function sessionFileNameForBranch(branch) {
  return `${sanitizeBranchForFile(branch)}.json`;
}

function activeSessionsDirForRepo(repoRoot) {
  return path.join(path.resolve(repoRoot), ACTIVE_SESSIONS_RELATIVE_DIR);
}

function sessionFilePathForBranch(repoRoot, branch) {
  return path.join(activeSessionsDirForRepo(repoRoot), sessionFileNameForBranch(branch));
}

function resolveManagedWorktreeRoots(repoRoot) {
  return MANAGED_WORKTREE_ROOTS.map((relativeRoot) => path.join(path.resolve(repoRoot), relativeRoot));
}

function splitOutputLines(output) {
  if (typeof output !== 'string') {
    return null;
  }

  return output
    .split(/\r?\n/)
    .filter((line) => line.trim().length > 0);
}

function normalizeRelativePath(value) {
  return toNonEmptyString(value).replace(/\\/g, '/').replace(/^\.\//, '');
}

function readJsonFile(filePath) {
  try {
    return JSON.parse(fs.readFileSync(filePath, 'utf8'));
  } catch (_error) {
    return null;
  }
}

function readConfiguredBaseBranch(repoRoot) {
  const lines = runGitLines(path.resolve(repoRoot), ['config', '--get', 'multiagent.baseBranch']);
  if (Array.isArray(lines) && typeof lines[0] === 'string' && lines[0].trim()) {
    return lines[0].trim();
  }
  return DEFAULT_BASE_BRANCH;
}

function readAheadBehindCounts(worktreePath, branch, baseBranch) {
  const normalizedWorktreePath = toNonEmptyString(worktreePath);
  const normalizedBranch = toNonEmptyString(branch);
  const normalizedBaseBranch = toNonEmptyString(baseBranch, DEFAULT_BASE_BRANCH);
  const compareRef = `origin/${normalizedBaseBranch}`;

  if (!normalizedWorktreePath || !normalizedBranch) {
    return {
      compareRef,
      aheadCount: null,
      behindCount: null,
    };
  }

  const lines = runGitLines(normalizedWorktreePath, [
    'rev-list',
    '--left-right',
    '--count',
    `${normalizedBranch}...${compareRef}`,
  ]);
  const match = Array.isArray(lines) && typeof lines[0] === 'string'
    ? lines[0].trim().match(/^(\d+)\s+(\d+)$/)
    : null;
  if (!match) {
    return {
      compareRef,
      aheadCount: null,
      behindCount: null,
    };
  }

  return {
    compareRef,
    aheadCount: Number.parseInt(match[1], 10),
    behindCount: Number.parseInt(match[2], 10),
  };
}

function sessionLogPath(repoRoot, branch) {
  const normalizedRepoRoot = toNonEmptyString(repoRoot);
  const normalizedBranch = toNonEmptyString(branch);
  if (!normalizedRepoRoot || !normalizedBranch) {
    return '';
  }

  return path.join(
    path.resolve(normalizedRepoRoot),
    LOGS_RELATIVE_DIR,
    `agent-${sanitizeBranchForFile(normalizedBranch)}.log`,
  );
}

function readLogTail(filePath, maxLines = DEFAULT_LOG_TAIL_LINE_COUNT) {
  const normalizedFilePath = toNonEmptyString(filePath);
  const normalizedMaxLines = toPositiveInteger(maxLines) || DEFAULT_LOG_TAIL_LINE_COUNT;
  if (!normalizedFilePath || !fs.existsSync(normalizedFilePath)) {
    return [];
  }

  try {
    const lines = fs.readFileSync(normalizedFilePath, 'utf8').split(/\r?\n/);
    while (lines.length > 0 && lines[lines.length - 1] === '') {
      lines.pop();
    }
    return lines.slice(-normalizedMaxLines);
  } catch (_error) {
    return [];
  }
}

function readSessionHeldLocks(repoRoot, branch) {
  const normalizedRepoRoot = toNonEmptyString(repoRoot);
  const normalizedBranch = toNonEmptyString(branch);
  if (!normalizedRepoRoot || !normalizedBranch) {
    return [];
  }

  const parsed = readJsonFile(path.join(path.resolve(normalizedRepoRoot), LOCK_FILE_RELATIVE));
  const locks = parsed?.locks;
  if (!locks || typeof locks !== 'object' || Array.isArray(locks)) {
    return [];
  }

  return Object.entries(locks)
    .map(([rawRelativePath, entry]) => {
      if (!entry || typeof entry !== 'object') {
        return null;
      }

      const relativePath = normalizeRelativePath(rawRelativePath);
      const ownerBranch = toNonEmptyString(entry.branch);
      if (!relativePath || ownerBranch !== normalizedBranch) {
        return null;
      }

      return {
        relativePath,
        claimedAt: toNonEmptyString(entry.claimed_at),
        allowDelete: Boolean(entry.allow_delete),
      };
    })
    .filter(Boolean)
    .sort((left, right) => left.relativePath.localeCompare(right.relativePath));
}

function readSessionInspectData(session, options = {}) {
  const repoRoot = toNonEmptyString(session?.repoRoot);
  const branch = toNonEmptyString(session?.branch);
  const worktreePath = toNonEmptyString(session?.worktreePath);
  const baseBranch = readConfiguredBaseBranch(repoRoot);
  const logPath = sessionLogPath(repoRoot, branch);
  const logTailLines = readLogTail(logPath, options.logLines);

  return {
    baseBranch,
    logPath,
    logExists: Boolean(logPath) && fs.existsSync(logPath),
    logTailLines,
    logTailText: logTailLines.join('\n'),
    heldLocks: readSessionHeldLocks(repoRoot, branch),
    ...readAheadBehindCounts(worktreePath, branch, baseBranch),
  };
}

function normalizeIsoString(value, fallback = '') {
  const normalized = toNonEmptyString(value);
  if (!normalized) {
    return fallback;
  }

  const timestamp = Date.parse(normalized);
  return Number.isFinite(timestamp) ? new Date(timestamp).toISOString() : fallback;
}

function runGitLines(worktreePath, args) {
  try {
    const output = cp.execFileSync('git', ['-C', worktreePath, ...args], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    });
    return splitOutputLines(output);
  } catch (_error) {
    return null;
  }
}

function unquoteGitPath(value) {
  if (typeof value !== 'string') {
    return '';
  }

  const trimmed = value.trim();
  if (!trimmed.startsWith('"') || !trimmed.endsWith('"')) {
    return trimmed;
  }

  try {
    return JSON.parse(trimmed);
  } catch (_error) {
    return trimmed.slice(1, -1);
  }
}

function formatFileCount(count) {
  return `${count} file${count === 1 ? '' : 's'}`;
}

function previewChangedPaths(paths) {
  if (!Array.isArray(paths) || paths.length === 0) {
    return '';
  }

  if (paths.length <= MAX_CHANGED_PATH_PREVIEW) {
    return paths.join(', ');
  }

  const preview = paths.slice(0, MAX_CHANGED_PATH_PREVIEW).join(', ');
  return `${preview}, +${paths.length - MAX_CHANGED_PATH_PREVIEW} more`;
}

function deriveRepoChangeStatus(statusPair) {
  if (statusPair === '??') {
    return {
      statusCode: '??',
      statusLabel: 'U',
      statusText: 'Untracked',
    };
  }

  const code = [statusPair[1], statusPair[0]].find((value) => value && value !== ' ') || 'M';
  const statusTextByCode = {
    A: 'Added',
    C: 'Copied',
    D: 'Deleted',
    M: 'Modified',
    R: 'Renamed',
    T: 'Type changed',
    U: 'Conflicted',
  };

  return {
    statusCode: code,
    statusLabel: code,
    statusText: statusTextByCode[code] || 'Changed',
  };
}

function parseRepoChangeLine(repoRoot, line) {
  if (typeof line !== 'string' || line.length < 4) {
    return null;
  }

  const statusPair = line.slice(0, 2);
  if (statusPair === '!!') {
    return null;
  }

  const rawPath = line.slice(3).trim();
  if (!rawPath) {
    return null;
  }

  let relativePath = rawPath;
  let originalPath = '';
  if (rawPath.includes(' -> ')) {
    const parts = rawPath.split(' -> ');
    if (parts.length === 2) {
      originalPath = unquoteGitPath(parts[0]);
      relativePath = parts[1];
    }
  }

  relativePath = unquoteGitPath(relativePath);
  if (!relativePath) {
    return null;
  }

  const normalizedRelativePath = relativePath.split(path.sep).join('/');
  if (
    normalizedRelativePath === LOCK_FILE_FILTER_PATH
    || normalizedRelativePath.startsWith(`${LOCK_FILE_FILTER_PATH}/`)
    || normalizedRelativePath === ACTIVE_SESSIONS_FILTER_PREFIX
    || normalizedRelativePath.startsWith(`${ACTIVE_SESSIONS_FILTER_PREFIX}/`)
    || MANAGED_WORKTREE_FILTER_PREFIXES.some((prefix) => (
      normalizedRelativePath === prefix || normalizedRelativePath.startsWith(`${prefix}/`)
    ))
  ) {
    return null;
  }

  const status = deriveRepoChangeStatus(statusPair);
  return {
    ...status,
    originalPath,
    relativePath,
    absolutePath: path.join(path.resolve(repoRoot), relativePath),
  };
}

function collectWorktreeChangedPaths(worktreePath) {
  const changedGroups = [
    runGitLines(worktreePath, ['diff', '--name-only', '--', '.', `:(exclude)${LOCK_FILE_RELATIVE}`, `:(exclude)${AGENT_WORKTREE_LOCK_FILE}`]),
    runGitLines(worktreePath, ['diff', '--cached', '--name-only', '--', '.', `:(exclude)${LOCK_FILE_RELATIVE}`, `:(exclude)${AGENT_WORKTREE_LOCK_FILE}`]),
    runGitLines(worktreePath, ['ls-files', '--others', '--exclude-standard']),
  ];

  if (changedGroups.some((group) => group === null)) {
    return null;
  }

  return [...new Set(changedGroups.flat())]
    .filter((relativePath) => (
      relativePath
      && relativePath !== LOCK_FILE_RELATIVE
      && relativePath !== AGENT_WORKTREE_LOCK_FILE
    ))
    .sort((left, right) => left.localeCompare(right));
}

function resolveWorktreeGitDir(worktreePath) {
  const gitPath = path.join(path.resolve(worktreePath), '.git');
  try {
    if (fs.statSync(gitPath).isDirectory()) {
      return gitPath;
    }
  } catch (_error) {
    return null;
  }

  try {
    const gitPointer = fs.readFileSync(gitPath, 'utf8');
    const match = gitPointer.match(/^gitdir:\s*(.+)$/m);
    if (match?.[1]) {
      return path.resolve(worktreePath, match[1].trim());
    }
  } catch (_error) {
    return null;
  }

  return null;
}

function deriveBlockingGitLabel(worktreePath) {
  const gitDir = resolveWorktreeGitDir(worktreePath);
  if (!gitDir) {
    return '';
  }

  for (const blockingState of BLOCKING_GIT_STATES) {
    if (blockingState.markers.some((marker) => fs.existsSync(path.join(gitDir, marker)))) {
      return blockingState.label;
    }
  }

  return '';
}

function collectWorktreeTrackedPaths(worktreePath) {
  const trackedPaths = runGitLines(worktreePath, ['ls-files', '--cached', '--others', '--exclude-standard']);
  if (!trackedPaths) {
    return null;
  }

  return [...new Set(trackedPaths)]
    .filter(Boolean)
    .sort((left, right) => left.localeCompare(right));
}

function shouldSkipWorktreeActivityPath(relativePath) {
  const normalized = normalizeRelativePath(relativePath);
  if (!normalized || normalized === LOCK_FILE_RELATIVE || normalized === AGENT_WORKTREE_LOCK_FILE) {
    return true;
  }

  return WORKTREE_ACTIVITY_SKIP_PREFIXES.some((prefix) => (
    normalized === prefix.slice(0, -1) || normalized.startsWith(prefix)
  ));
}

function worktreeActivityPathPriority(relativePath, recentPathsSet) {
  if (recentPathsSet.has(relativePath)) {
    return 0;
  }
  if (!relativePath.includes('/')) {
    return 1;
  }
  if (WORKTREE_ACTIVITY_PRIORITY_PREFIXES.some((prefix) => relativePath.startsWith(prefix))) {
    return 2;
  }
  return 3;
}

function collectWorktreeActivityCandidatePaths(worktreePath, trackedPaths) {
  const recentPaths = runGitLines(worktreePath, ['log', '-1', '--name-only', '--pretty=format:', '--', '.']) || [];
  const filteredRecentPaths = [...new Set(recentPaths.map(normalizeRelativePath).filter(Boolean))]
    .filter((relativePath) => !shouldSkipWorktreeActivityPath(relativePath));
  const recentPathSet = new Set(filteredRecentPaths);
  const prioritizedTrackedPaths = trackedPaths
    .map(normalizeRelativePath)
    .filter(Boolean)
    .filter((relativePath) => !shouldSkipWorktreeActivityPath(relativePath))
    .sort((left, right) => {
      const priorityDelta = worktreeActivityPathPriority(left, recentPathSet)
        - worktreeActivityPathPriority(right, recentPathSet);
      if (priorityDelta !== 0) {
        return priorityDelta;
      }
      return left.localeCompare(right);
    });

  return [...new Set([...filteredRecentPaths, ...prioritizedTrackedPaths])]
    .slice(0, MAX_WORKTREE_ACTIVITY_STAT_PATHS);
}

function clearWorktreeActivityCache(worktreePath = '') {
  const normalizedWorktreePath = toNonEmptyString(worktreePath);
  if (!normalizedWorktreePath) {
    worktreeActivityCache.clear();
    return;
  }
  worktreeActivityCache.delete(path.resolve(normalizedWorktreePath));
}

function deriveLatestWorktreeFileActivity(worktreePath, options = {}) {
  const now = Number.isFinite(options.now) ? options.now : Date.now();
  const useCache = options.useCache !== false;
  const cacheKey = path.resolve(worktreePath);
  if (useCache) {
    const cached = worktreeActivityCache.get(cacheKey);
    if (cached && (now - cached.checkedAtMs) < WORKTREE_ACTIVITY_CACHE_TTL_MS) {
      return cached.latestMtimeMs;
    }
  }

  const trackedPaths = collectWorktreeTrackedPaths(worktreePath);
  if (!trackedPaths) {
    return null;
  }

  const candidatePaths = collectWorktreeActivityCandidatePaths(worktreePath, trackedPaths);
  let latestMtimeMs = null;
  for (const relativePath of candidatePaths) {
    const absolutePath = path.join(worktreePath, relativePath);
    try {
      const stats = fs.statSync(absolutePath);
      if (!stats.isFile() || !Number.isFinite(stats.mtimeMs)) {
        continue;
      }
      latestMtimeMs = latestMtimeMs === null
        ? stats.mtimeMs
        : Math.max(latestMtimeMs, stats.mtimeMs);
    } catch (_error) {
      continue;
    }
  }

  if (useCache) {
    worktreeActivityCache.set(cacheKey, {
      checkedAtMs: now,
      latestMtimeMs,
    });
  }

  return latestMtimeMs;
}

function deriveSessionActivity(session, options = {}) {
  const now = Number.isFinite(options.now) ? options.now : Date.now();
  const pid = toPositiveInteger(session?.pid);
  const pidAlive = pid ? isPidAlive(pid) : null;
  const heartbeatAt = normalizeIsoString(session?.lastHeartbeatAt);
  const heartbeatMs = Date.parse(heartbeatAt);
  if (heartbeatAt && Number.isFinite(heartbeatMs) && now - heartbeatMs > HEARTBEAT_STALE_MS) {
    return {
      activityKind: 'dead',
      activityLabel: 'dead',
      activityCountLabel: '',
      activitySummary: `Heartbeat stale for ${formatElapsedFrom(heartbeatAt, now)}.`,
      changeCount: 0,
      changedPaths: [],
      worktreeChangedPaths: [],
      pidAlive,
      lastFileActivityAt: '',
      lastFileActivityLabel: '',
    };
  }

  const blockingLabel = deriveBlockingGitLabel(session.worktreePath);
  if (blockingLabel) {
    return {
      activityKind: 'blocked',
      activityLabel: 'blocked',
      activityCountLabel: '',
      activitySummary: blockingLabel,
      changeCount: 0,
      changedPaths: [],
      worktreeChangedPaths: [],
      pidAlive,
      lastFileActivityAt: '',
      lastFileActivityLabel: '',
    };
  }

  if (pid && !pidAlive) {
    return {
      activityKind: 'dead',
      activityLabel: 'dead',
      activityCountLabel: '',
      activitySummary: 'Recorded PID is not alive.',
      changeCount: 0,
      changedPaths: [],
      worktreeChangedPaths: [],
      pidAlive,
      lastFileActivityAt: '',
      lastFileActivityLabel: '',
    };
  }

  const worktreeChangedPaths = collectWorktreeChangedPaths(session.worktreePath);
  if (!worktreeChangedPaths) {
    return {
      activityKind: 'idle',
      activityLabel: 'idle',
      activityCountLabel: '',
      activitySummary: 'Worktree activity unavailable.',
      changeCount: 0,
      changedPaths: [],
      worktreeChangedPaths: [],
      pidAlive,
      lastFileActivityAt: '',
      lastFileActivityLabel: '',
    };
  }

  if (worktreeChangedPaths.length > 0) {
    const worktreeRelativePaths = [...new Set(worktreeChangedPaths
      .map((relativePath) => normalizeRelativePath(relativePath))
      .filter(Boolean))]
      .sort((left, right) => left.localeCompare(right));
    clearWorktreeActivityCache(session.worktreePath);
    const changedPaths = [...new Set(worktreeChangedPaths
      .map((relativePath) => normalizeRelativePath(
        path.relative(session.repoRoot, path.resolve(session.worktreePath, relativePath)),
      ))
      .filter(Boolean))]
      .sort((left, right) => left.localeCompare(right));

    return {
      activityKind: 'working',
      activityLabel: 'working',
      activityCountLabel: formatFileCount(worktreeChangedPaths.length),
      activitySummary: previewChangedPaths(worktreeChangedPaths),
      changeCount: worktreeChangedPaths.length,
      changedPaths,
      worktreeChangedPaths: worktreeRelativePaths,
      pidAlive,
      lastFileActivityAt: '',
      lastFileActivityLabel: '',
    };
  }

  const latestFileActivityMs = deriveLatestWorktreeFileActivity(session.worktreePath, {
    now,
    useCache: options.useCache,
  });
  const lastFileActivityAt = Number.isFinite(latestFileActivityMs)
    ? new Date(latestFileActivityMs).toISOString()
    : '';
  const lastFileActivityLabel = lastFileActivityAt
    ? formatElapsedFrom(lastFileActivityAt, now)
    : '';
  const lastFileActivityAgeMs = Number.isFinite(latestFileActivityMs)
    ? Math.max(0, now - latestFileActivityMs)
    : null;

  if (lastFileActivityAgeMs !== null && lastFileActivityAgeMs > STALLED_ACTIVITY_WINDOW_MS) {
    return {
      activityKind: 'stalled',
      activityLabel: 'stalled',
      activityCountLabel: '',
      activitySummary: `Worktree clean. No file activity for ${lastFileActivityLabel}.`,
      changeCount: 0,
      changedPaths: [],
      worktreeChangedPaths: [],
      pidAlive,
      lastFileActivityAt,
      lastFileActivityLabel,
    };
  }

  return {
    activityKind: 'idle',
    activityLabel: 'idle',
    activityCountLabel: '',
    activitySummary: lastFileActivityAgeMs !== null && lastFileActivityAgeMs <= IDLE_ACTIVITY_WINDOW_MS
      ? `Worktree clean. Recent file activity ${lastFileActivityLabel} ago.`
      : lastFileActivityLabel
        ? `Worktree clean. Last file activity ${lastFileActivityLabel} ago.`
        : 'Worktree clean.',
    changeCount: 0,
    changedPaths: [],
    worktreeChangedPaths: [],
    pidAlive,
    lastFileActivityAt,
    lastFileActivityLabel,
  };
}

function buildSessionRecord(input) {
  const repoRoot = path.resolve(toNonEmptyString(input.repoRoot));
  const worktreePath = path.resolve(toNonEmptyString(input.worktreePath));
  const branch = toNonEmptyString(input.branch);
  const pid = toPositiveInteger(input.pid);
  const startedAt = input.startedAt ? new Date(input.startedAt) : new Date();
  const lastHeartbeatAt = input.lastHeartbeatAt ? new Date(input.lastHeartbeatAt) : new Date();

  if (!branch) {
    throw new Error('branch is required');
  }
  if (!repoRoot) {
    throw new Error('repoRoot is required');
  }
  if (!worktreePath) {
    throw new Error('worktreePath is required');
  }
  if (!pid) {
    throw new Error('pid must be a positive integer');
  }
  if (Number.isNaN(startedAt.getTime())) {
    throw new Error('startedAt must be a valid date');
  }
  if (Number.isNaN(lastHeartbeatAt.getTime())) {
    throw new Error('lastHeartbeatAt must be a valid date');
  }

  return {
    schemaVersion: SESSION_SCHEMA_VERSION,
    repoRoot,
    branch,
    taskName: toNonEmptyString(input.taskName, 'task'),
    latestTaskPreview: '',
    agentName: toNonEmptyString(input.agentName, 'agent'),
    worktreePath,
    pid,
    cliName: toNonEmptyString(input.cliName, 'codex'),
    taskMode: normalizeTaskMode(input.taskMode),
    openspecTier: normalizeOpenSpecTier(input.openspecTier),
    taskRoutingReason: toNonEmptyString(input.taskRoutingReason),
    startedAt: startedAt.toISOString(),
    lastHeartbeatAt: lastHeartbeatAt.toISOString(),
    state: normalizeAdvisoryState(input.state),
  };
}

function deriveSessionLabel(branch, worktreePath) {
  const worktreeLeaf = toNonEmptyString(path.basename(worktreePath || ''));
  if (worktreeLeaf) {
    return worktreeLeaf;
  }
  return toNonEmptyString(branch).replace(/[\\/]+/g, '-') || 'unknown-agent';
}

function normalizeSessionRecord(input, options = {}) {
  if (!input || typeof input !== 'object') {
    return null;
  }

  const repoRoot = toNonEmptyString(input.repoRoot);
  const branch = toNonEmptyString(input.branch);
  const worktreePath = toNonEmptyString(input.worktreePath);
  const startedAt = new Date(input.startedAt);
  const lastHeartbeatAt = new Date(input.lastHeartbeatAt || input.startedAt);
  const pid = toPositiveInteger(input.pid);

  if (
    !repoRoot
    || !branch
    || !worktreePath
    || !pid
    || Number.isNaN(startedAt.getTime())
    || Number.isNaN(lastHeartbeatAt.getTime())
  ) {
    return null;
  }

  return {
    schemaVersion: toPositiveInteger(input.schemaVersion) || SESSION_SCHEMA_VERSION,
    repoRoot: path.resolve(repoRoot),
    branch,
    taskName: toNonEmptyString(input.taskName, 'task'),
    latestTaskPreview: '',
    agentName: toNonEmptyString(input.agentName, 'agent'),
    worktreePath: path.resolve(worktreePath),
    pid,
    cliName: toNonEmptyString(input.cliName, 'codex'),
    taskMode: normalizeTaskMode(input.taskMode),
    openspecTier: normalizeOpenSpecTier(input.openspecTier),
    taskRoutingReason: toNonEmptyString(input.taskRoutingReason),
    startedAt: startedAt.toISOString(),
    lastHeartbeatAt: lastHeartbeatAt.toISOString(),
    state: normalizeAdvisoryState(input.state, 'idle'),
    filePath: toNonEmptyString(options.filePath),
    label: deriveSessionLabel(branch, worktreePath),
    changedPaths: [],
    worktreeChangedPaths: [],
    sourceKind: 'active-session',
    telemetryUpdatedAt: '',
    telemetrySource: '',
    lockSnapshotCount: 0,
    lockSessionCount: 0,
    collaboration: false,
  };
}

function formatElapsedFrom(startedAt, now = Date.now()) {
  const startedAtMs = startedAt instanceof Date ? startedAt.getTime() : Date.parse(startedAt);
  if (!Number.isFinite(startedAtMs)) {
    return '0s';
  }

  const totalSeconds = Math.max(0, Math.floor((now - startedAtMs) / 1000));
  const days = Math.floor(totalSeconds / 86_400);
  const hours = Math.floor((totalSeconds % 86_400) / 3_600);
  const minutes = Math.floor((totalSeconds % 3_600) / 60);
  const seconds = totalSeconds % 60;

  if (days > 0) {
    return `${days}d ${hours}h`;
  }
  if (hours > 0) {
    return `${hours}h ${minutes}m`;
  }
  if (minutes > 0) {
    return `${minutes}m ${seconds}s`;
  }
  return `${seconds}s`;
}

function isPidAlive(pid) {
  const normalizedPid = toPositiveInteger(pid);
  if (!normalizedPid) {
    return false;
  }

  try {
    process.kill(normalizedPid, 0);
    return true;
  } catch (_error) {
    return false;
  }
}

function readWorktreeBranch(worktreePath) {
  const lines = runGitLines(worktreePath, ['rev-parse', '--abbrev-ref', 'HEAD']);
  return Array.isArray(lines) && typeof lines[0] === 'string' ? lines[0].trim() : '';
}

function deriveAgentNameFromBranch(branch) {
  const parts = toNonEmptyString(branch).split('/').filter(Boolean);
  if (parts.length >= 2 && parts[0] === 'agent') {
    return parts[1];
  }
  return 'agent';
}

function flattenTelemetrySnapshotSessions(lockPayload) {
  const flattened = [];
  const snapshots = Array.isArray(lockPayload?.snapshots) ? lockPayload.snapshots : [];
  for (const snapshot of snapshots) {
    const snapshotSessions = Array.isArray(snapshot?.sessions) ? snapshot.sessions : [];
    for (const session of snapshotSessions) {
      flattened.push({
        taskPreview: toNonEmptyString(session?.taskPreview),
        taskUpdatedAt: normalizeIsoString(session?.taskUpdatedAt),
        projectName: toNonEmptyString(session?.projectName),
        projectPath: toNonEmptyString(session?.projectPath),
        snapshotName: toNonEmptyString(snapshot?.snapshotName),
        email: toNonEmptyString(snapshot?.email),
      });
    }
  }
  return flattened;
}

function sortSessionsByTimestamp(sessions) {
  sessions.sort((left, right) => {
    const timeDelta = Date.parse(right.startedAt) - Date.parse(left.startedAt);
    if (timeDelta !== 0) {
      return timeDelta;
    }
    return left.label.localeCompare(right.label);
  });
  return sessions;
}

function deriveLockTaskAnchor(entries, fallbackTaskName, fallbackTimestamp) {
  const sortedEntries = [...entries].sort((left, right) => {
    const timeDelta = Date.parse(right.taskUpdatedAt || '') - Date.parse(left.taskUpdatedAt || '');
    if (timeDelta !== 0) {
      return timeDelta;
    }
    if (Boolean(right.taskPreview) !== Boolean(left.taskPreview)) {
      return Number(Boolean(right.taskPreview)) - Number(Boolean(left.taskPreview));
    }
    return (right.projectPath || '').localeCompare(left.projectPath || '');
  });

  const latestEntry = sortedEntries[0] || null;
  return {
    taskName: latestEntry?.taskPreview || fallbackTaskName || 'task',
    latestTaskPreview: latestEntry?.taskPreview || '',
    timestamp: latestEntry?.taskUpdatedAt || fallbackTimestamp || '',
  };
}

function buildWorktreeLockSession(repoRoot, worktreePath, lockPayload, options = {}) {
  const now = options.now || Date.now();
  const telemetryEntries = flattenTelemetrySnapshotSessions(lockPayload);
  const telemetryUpdatedAt = normalizeIsoString(lockPayload?.updatedAt);
  const branch = readWorktreeBranch(worktreePath);
  const effectiveBranch = branch && branch !== 'HEAD'
    ? branch
    : `agent/telemetry/${path.basename(worktreePath)}`;
  const label = deriveSessionLabel(effectiveBranch, worktreePath);
  const taskAnchor = deriveLockTaskAnchor(telemetryEntries, label, telemetryUpdatedAt);
  const startedAt = taskAnchor.timestamp || telemetryUpdatedAt || new Date(now).toISOString();

  const session = {
    schemaVersion: toPositiveInteger(lockPayload?.schemaVersion) || SESSION_SCHEMA_VERSION,
    repoRoot: path.resolve(repoRoot),
    branch: effectiveBranch,
    taskName: taskAnchor.taskName,
    latestTaskPreview: taskAnchor.latestTaskPreview,
    agentName: deriveAgentNameFromBranch(effectiveBranch),
    worktreePath: path.resolve(worktreePath),
    pid: null,
    cliName: 'codex',
    taskMode: '',
    openspecTier: '',
    taskRoutingReason: '',
    startedAt,
    lastHeartbeatAt: '',
    state: '',
    filePath: path.join(worktreePath, AGENT_WORKTREE_LOCK_FILE),
    label,
    changedPaths: [],
    worktreeChangedPaths: [],
    sourceKind: 'worktree-lock',
    telemetryUpdatedAt: telemetryUpdatedAt || startedAt,
    telemetrySource: toNonEmptyString(lockPayload?.source, 'worktree-lock'),
    lockSnapshotCount: toPositiveInteger(lockPayload?.snapshotCount) || 0,
    lockSessionCount: toPositiveInteger(lockPayload?.sessionCount) || telemetryEntries.length,
    collaboration: Boolean(lockPayload?.collaboration),
  };

  session.elapsedLabel = formatElapsedFrom(session.startedAt, now);
  Object.assign(session, deriveSessionActivity(session, { now }));
  return session;
}

function readWorktreeLockSessions(repoRoot, options = {}) {
  const sessions = [];
  for (const managedRoot of resolveManagedWorktreeRoots(repoRoot)) {
    if (!fs.existsSync(managedRoot)) {
      continue;
    }

    let entries;
    try {
      entries = fs.readdirSync(managedRoot, { withFileTypes: true });
    } catch (_error) {
      continue;
    }

    for (const entry of entries) {
      if (!entry.isDirectory()) {
        continue;
      }

      const worktreePath = path.join(managedRoot, entry.name);
      const lockPath = path.join(worktreePath, AGENT_WORKTREE_LOCK_FILE);
      if (!fs.existsSync(lockPath)) {
        continue;
      }

      const lockPayload = readJsonFile(lockPath);
      if (!lockPayload || typeof lockPayload !== 'object' || Array.isArray(lockPayload)) {
        continue;
      }

      const telemetryEntries = flattenTelemetrySnapshotSessions(lockPayload);
      if (telemetryEntries.length === 0 && !toPositiveInteger(lockPayload.sessionCount)) {
        continue;
      }

      sessions.push(buildWorktreeLockSession(repoRoot, worktreePath, lockPayload, options));
    }
  }

  return sortSessionsByTimestamp(sessions);
}

function mergeSessionSources(primarySessions, lockSessions) {
  const lockSessionsByWorktree = new Map(
    lockSessions.map((session) => [path.resolve(session.worktreePath), session]),
  );
  const consumedLockWorktrees = new Set();
  const merged = [];

  for (const session of primarySessions) {
    const worktreeKey = path.resolve(session.worktreePath);
    const lockSession = lockSessionsByWorktree.get(worktreeKey);
    if (lockSession && session.activityKind === 'dead') {
      continue;
    }
    if (lockSession) {
      consumedLockWorktrees.add(worktreeKey);
    }
    merged.push(session);
  }

  for (const lockSession of lockSessions) {
    const worktreeKey = path.resolve(lockSession.worktreePath);
    if (!consumedLockWorktrees.has(worktreeKey)) {
      merged.push(lockSession);
    }
  }

  return sortSessionsByTimestamp(merged);
}

function readActiveSessions(repoRoot, options = {}) {
  const activeSessionsDir = activeSessionsDirForRepo(repoRoot);
  const now = options.now || Date.now();
  const sessionFileSessions = [];
  if (fs.existsSync(activeSessionsDir)) {
    for (const entry of fs.readdirSync(activeSessionsDir, { withFileTypes: true })) {
      if (!entry.isFile() || !entry.name.endsWith('.json')) {
        continue;
      }

      const filePath = path.join(activeSessionsDir, entry.name);
      const parsed = readJsonFile(filePath);
      const normalized = normalizeSessionRecord(parsed, { filePath });
      if (!normalized) {
        continue;
      }
      if (!options.includeStale && !isPidAlive(normalized.pid)) {
        continue;
      }

      normalized.elapsedLabel = formatElapsedFrom(normalized.startedAt, now);
      Object.assign(normalized, deriveSessionActivity(normalized, { now }));
      sessionFileSessions.push(normalized);
    }
  }

  return mergeSessionSources(
    sortSessionsByTimestamp(sessionFileSessions),
    readWorktreeLockSessions(repoRoot, { now }),
  );
}

function readRepoChanges(repoRoot) {
  const statusLines = runGitLines(repoRoot, ['status', '--porcelain=v1', '--untracked-files=all']);
  if (!statusLines) {
    return [];
  }

  return statusLines
    .map((line) => parseRepoChangeLine(repoRoot, line))
    .filter(Boolean)
    .sort((left, right) => left.relativePath.localeCompare(right.relativePath));
}

module.exports = {
  ACTIVE_SESSIONS_RELATIVE_DIR,
  SESSION_SCHEMA_VERSION,
  activeSessionsDirForRepo,
  buildSessionRecord,
  clearWorktreeActivityCache,
  collectWorktreeChangedPaths,
  collectWorktreeTrackedPaths,
  deriveBlockingGitLabel,
  deriveLatestWorktreeFileActivity,
  deriveSessionLabel,
  deriveSessionActivity,
  formatElapsedFrom,
  formatFileCount,
  isPidAlive,
  normalizeSessionRecord,
  parseRepoChangeLine,
  previewChangedPaths,
  readActiveSessions,
  readWorktreeLockSessions,
  readRepoChanges,
  deriveRepoChangeStatus,
  readAheadBehindCounts,
  readConfiguredBaseBranch,
  readLogTail,
  resolveWorktreeGitDir,
  readSessionHeldLocks,
  readSessionInspectData,
  sessionLogPath,
  sanitizeBranchForFile,
  sessionFileNameForBranch,
  sessionFilePathForBranch,
};
