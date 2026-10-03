'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');

const MCP_JSON_REL = 'mcp.json';

function cursorConfigDir(home) {
  return path.join(home, '.cursor');
}

function cavemanHome(home = os.homedir()) {
  return path.join(home, '.caveman');
}

function mcpMarkerPath(home, serverName) {
  const base = path.join(cavemanHome(home), 'mcp');
  return serverName === 'caveman'
    ? path.join(base, 'cursor.json')
    : path.join(base, `cursor.${serverName}.json`);
}

function readJsonObject(filePath) {
  try {
    const raw = fs.readFileSync(filePath, 'utf8').trim();
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      throw new Error('not a JSON object');
    }
    return parsed;
  } catch (error) {
    if (error.code === 'ENOENT') return {};
    throw error;
  }
}

function writeJsonObject(filePath, value) {
  fs.mkdirSync(path.dirname(filePath), { recursive: true, mode: 0o700 });
  fs.writeFileSync(filePath, JSON.stringify(value, null, 2) + '\n', { mode: 0o600 });
}

function readMcpMarker(home, serverName) {
  try {
    const value = JSON.parse(fs.readFileSync(mcpMarkerPath(home, serverName), 'utf8'));
    if (typeof value.command !== 'string' || !Array.isArray(value.args)) return null;
    if (!value.args.every((arg) => typeof arg === 'string')) return null;
    return { command: value.command, args: value.args };
  } catch (_) {
    return null;
  }
}

function writeMcpMarker(home, serverName, mcp, tool) {
  const target = mcpMarkerPath(home, serverName);
  fs.mkdirSync(path.dirname(target), { recursive: true, mode: 0o700 });
  fs.writeFileSync(target, JSON.stringify({ tool, command: mcp.command, args: mcp.args }, null, 2) + '\n', { mode: 0o600 });
}

function removeMcpMarker(home, serverName) {
  try { fs.unlinkSync(mcpMarkerPath(home, serverName)); } catch (error) {
    if (error.code !== 'ENOENT') throw error;
  }
}

function mcpEntryMatches(left, right) {
  return left.command === right.command && JSON.stringify(left.args) === JSON.stringify(right.args);
}

function mergeMcpServer(root, home, serverName, mcp, force, warn) {
  const filePath = path.join(root, MCP_JSON_REL);
  let rootObj;
  try {
    rootObj = readJsonObject(filePath);
  } catch (error) {
    warn(`  ${filePath}: ${error.message}; skipped ${serverName} MCP`);
    return false;
  }
  if (!rootObj.mcpServers || typeof rootObj.mcpServers !== 'object' || Array.isArray(rootObj.mcpServers)) {
    rootObj.mcpServers = {};
  }
  const servers = rootObj.mcpServers;
  const next = { command: mcp.command, args: mcp.args };
  const existing = servers[serverName];
  const marker = readMcpMarker(home, serverName);
  if (existing && typeof existing === 'object' && !Array.isArray(existing)) {
    const cur = {
      command: typeof existing.command === 'string' ? existing.command : '',
      args: Array.isArray(existing.args) && existing.args.every((a) => typeof a === 'string') ? existing.args : [],
    };
    if (mcpEntryMatches(cur, next)) {
      writeMcpMarker(
        home,
        serverName,
        mcp,
        serverName === 'caveman' ? 'caveman_retrieve' : serverName === 'cavemem' ? 'cavemem_recall' : 'caveman-shrink',
      );
      return true;
    }
    if (!marker && !force) {
      warn(`  ${filePath} mcpServers.${serverName} exists and is not Caveman-owned; left unchanged`);
      return false;
    }
    if (!marker && force) {
      warn(`  backed up conflicting mcpServers.${serverName} before overwrite (--force)`);
    }
  }
  servers[serverName] = next;
  writeJsonObject(filePath, rootObj);
  writeMcpMarker(
    home,
    serverName,
    mcp,
    serverName === 'caveman' ? 'caveman_retrieve' : serverName === 'cavemem' ? 'cavemem_recall' : 'caveman-shrink',
  );
  return true;
}

function unmergeMcpServer(root, home, serverName) {
  const filePath = path.join(root, MCP_JSON_REL);
  let rootObj;
  try {
    rootObj = readJsonObject(filePath);
  } catch (_) {
    removeMcpMarker(home, serverName);
    return;
  }
  if (!rootObj || !rootObj.mcpServers || typeof rootObj.mcpServers !== 'object') {
    removeMcpMarker(home, serverName);
    return;
  }
  const marker = readMcpMarker(home, serverName);
  const existing = rootObj.mcpServers[serverName];
  if (!existing) {
    removeMcpMarker(home, serverName);
    return;
  }
  if (marker) {
    const cur = {
      command: typeof existing.command === 'string' ? existing.command : '',
      args: Array.isArray(existing.args) && existing.args.every((a) => typeof a === 'string') ? existing.args : [],
    };
    if (!mcpEntryMatches(cur, marker)) {
      return;
    }
  } else {
    return;
  }
  delete rootObj.mcpServers[serverName];
  if (Object.keys(rootObj.mcpServers).length === 0) delete rootObj.mcpServers;
  if (Object.keys(rootObj).length === 0) {
    try { fs.unlinkSync(filePath); } catch (error) { if (error.code !== 'ENOENT') throw error; }
  } else {
    writeJsonObject(filePath, rootObj);
  }
  removeMcpMarker(home, serverName);
}

function installCursorMcpJson(mcp, { home = os.homedir(), force = false, warn = () => {} } = {}) {
  const root = cursorConfigDir(home);
  return mergeMcpServer(root, home, 'caveman', mcp, force, warn);
}

function uninstallCursorMcpJson(serverName = 'caveman', { home = os.homedir() } = {}) {
  const root = cursorConfigDir(home);
  unmergeMcpServer(root, home, serverName);
  return true;
}

module.exports = {
  MCP_JSON_REL,
  cursorConfigDir,
  installCursorMcpJson,
  mergeMcpServer,
  mcpMarkerPath,
  readMcpMarker,
  unmergeMcpServer,
  uninstallCursorMcpJson,
};
