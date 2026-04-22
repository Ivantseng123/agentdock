const fs = require('node:fs');
const path = require('node:path');

const OBSERVED_ALLOWED_KEYS = new Set([
  'capabilities',
  'categories',
  'custom_field_options',
  'enum_values',
  'fields',
  'handlers',
  'known_quirks',
  'project_members',
  'project_rules',
  'required_fields',
  'statuses',
  'validated_capabilities',
  'validated_examples',
]);

function defaultSkillRoot() {
  return path.resolve(__dirname, '..', '..');
}

function defaultWorkspaceRoot() {
  return defaultSkillRoot();
}

function normalizeProjectId(projectId) {
  if (typeof projectId === 'number' && Number.isInteger(projectId) && projectId >= 0) {
    return String(projectId);
  }

  if (typeof projectId === 'string' && /^\d+$/.test(projectId)) {
    return projectId;
  }

  throw new Error(`Invalid project id: ${projectId}`);
}

function getMetadataPaths({ skillRoot, workspaceRoot, metadataRoot, projectId } = {}) {
  const resolvedSkillRoot = path.resolve(skillRoot || workspaceRoot || defaultSkillRoot());
  const resolvedMetadataRoot = path.resolve(metadataRoot || path.join(resolvedSkillRoot, 'metadata'));
  const normalizedProjectId = normalizeProjectId(projectId);

  return {
    skillRoot: resolvedSkillRoot,
    metadataRoot: resolvedMetadataRoot,
    indexPath: path.join(resolvedMetadataRoot, 'index.json'),
    projectPath: path.join(resolvedMetadataRoot, 'projects', `${normalizedProjectId}.json`),
    observedPath: path.join(resolvedMetadataRoot, 'observed', `${normalizedProjectId}.json`),
    projectId: normalizedProjectId,
    workspaceRoot: resolvedSkillRoot,
  };
}

function getWorkspaceMetadataPaths({ workspaceRoot, ...rest } = {}) {
  return getMetadataPaths({ workspaceRoot, ...rest });
}

function readJsonFile(filePath) {
  try {
    return JSON.parse(fs.readFileSync(filePath, 'utf8'));
  } catch (error) {
    if (error.code === 'ENOENT') {
      return null;
    }
    throw error;
  }
}

function writeJsonFile(filePath, value) {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  fs.writeFileSync(filePath, `${JSON.stringify(value, null, 2)}\n`, 'utf8');
}

function normalizeObservedEntries(entries, { source, observedAt }) {
  if (!entries || typeof entries !== 'object' || Array.isArray(entries)) {
    throw new Error('Observed metadata entries must be an object');
  }

  const normalized = {};
  for (const [key, value] of Object.entries(entries)) {
    if (!OBSERVED_ALLOWED_KEYS.has(key)) {
      throw new Error(`Observed metadata only accepts structured keys; rejected free-form key: ${key}`);
    }

    normalized[key] = {
      value,
      source,
      observed_at: observedAt,
      status: 'valid',
      reason: '',
    };
  }

  return normalized;
}

function upsertProjectIndexEntry(indexDoc, { projectId, refreshedAt = new Date().toISOString() }) {
  const doc = indexDoc && typeof indexDoc === 'object' ? indexDoc : { projects: [] };
  let projects = doc.projects;
  if (!Array.isArray(projects)) {
    projects = [];
    doc.projects = projects;
  }
  const normalizedProjectId = normalizeProjectId(projectId);
  const numericProjectId = Number(normalizedProjectId);
  const nextPath = `projects/${normalizedProjectId}.json`;

  const existing = projects.find((entry) => String(entry.id) === String(normalizedProjectId));
  if (existing) {
    const changed = existing.last_refreshed_at !== refreshedAt;
    existing.id = numericProjectId;
    existing.path = existing.path || nextPath;
    existing.last_refreshed_at = refreshedAt;
    return { changed, entry: existing };
  }

  const entry = {
    id: numericProjectId,
    path: nextPath,
    last_refreshed_at: refreshedAt,
  };

  projects.push(entry);
  return { changed: true, entry };
}

function readObservedMetadata({ skillRoot, workspaceRoot, metadataRoot, projectId }) {
  const { observedPath } = getMetadataPaths({ skillRoot, workspaceRoot, metadataRoot, projectId });
  return readJsonFile(observedPath);
}

function readWorkspaceObservedMetadata({ workspaceRoot, projectId }) {
  return readObservedMetadata({ workspaceRoot, projectId });
}

function readAuthoritativeMetadata({ skillRoot, workspaceRoot, metadataRoot, projectId }) {
  const { projectPath } = getMetadataPaths({ skillRoot, workspaceRoot, metadataRoot, projectId });
  return readJsonFile(projectPath);
}

function readWorkspaceAuthoritativeMetadata({ workspaceRoot, projectId }) {
  return readAuthoritativeMetadata({ workspaceRoot, projectId });
}


function writeObservedMetadata({
  skillRoot,
  workspaceRoot,
  metadataRoot,
  projectId,
  entries,
  source = 'api_observed',
  observedAt = new Date().toISOString(),
}) {
  const paths = getMetadataPaths({ skillRoot, workspaceRoot, metadataRoot, projectId });
  const existing = readObservedMetadata({ skillRoot, workspaceRoot, metadataRoot, projectId }) || {
    schema_version: 1,
    project: { id: Number(paths.projectId) },
    entries: {},
  };

  Object.assign(existing.entries, normalizeObservedEntries(entries, { source, observedAt }));
  existing.updated_at = observedAt;

  writeJsonFile(paths.observedPath, existing);
  return existing;
}

function writeWorkspaceObservedMetadata({ workspaceRoot, projectId, entries, source, observedAt }) {
  return writeObservedMetadata({ workspaceRoot, projectId, entries, source, observedAt });
}

function resolveMetadataValue({ skillRoot, workspaceRoot, metadataRoot, projectId, key }) {
  const authoritative = readAuthoritativeMetadata({ skillRoot, workspaceRoot, metadataRoot, projectId });
  if (authoritative && authoritative[key] !== undefined) {
    return { source: 'authoritative', value: authoritative[key] };
  }

  const observed = readObservedMetadata({ skillRoot, workspaceRoot, metadataRoot, projectId });
  const entry = observed?.entries?.[key];
  if (entry && entry.status === 'valid') {
    return { source: 'observed', value: entry.value };
  }

  return null;
}

function resolveWorkspaceMetadataValue({ workspaceRoot, projectId, key }) {
  return resolveMetadataValue({ workspaceRoot, projectId, key });
}

function markObservedMetadataStale({ skillRoot, workspaceRoot, metadataRoot, projectId, staleAt = new Date().toISOString(), reason = 'refresh' }) {
  const observed = readObservedMetadata({ skillRoot, workspaceRoot, metadataRoot, projectId });
  if (!observed) {
    return null;
  }

  for (const entry of Object.values(observed.entries || {})) {
    if (!entry || entry.status === 'invalid') {
      continue;
    }

    entry.status = 'stale';
    entry.reason = reason;
    entry.stale_at = staleAt;
  }

  observed.updated_at = staleAt;
  const { observedPath } = getMetadataPaths({ skillRoot, workspaceRoot, metadataRoot, projectId });
  writeJsonFile(observedPath, observed);
  return observed;
}

function markWorkspaceObservedMetadataStale({ workspaceRoot, projectId, staleAt, reason }) {
  return markObservedMetadataStale({ workspaceRoot, projectId, staleAt, reason });
}

function invalidateObservedMetadata({
  skillRoot,
  workspaceRoot,
  metadataRoot,
  projectId,
  key,
  invalidatedAt = new Date().toISOString(),
  reason = 'api_rejected',
}) {
  const observed = readObservedMetadata({ skillRoot, workspaceRoot, metadataRoot, projectId });
  if (!observed?.entries?.[key]) {
    return null;
  }

  observed.entries[key].status = 'invalid';
  observed.entries[key].reason = reason;
  observed.entries[key].invalidated_at = invalidatedAt;
  observed.updated_at = invalidatedAt;

  const { observedPath } = getMetadataPaths({ skillRoot, workspaceRoot, metadataRoot, projectId });
  writeJsonFile(observedPath, observed);
  return observed;
}

function invalidateWorkspaceObservedMetadata({ workspaceRoot, projectId, key, invalidatedAt, reason }) {
  return invalidateObservedMetadata({ workspaceRoot, projectId, key, invalidatedAt, reason });
}

module.exports = {
  OBSERVED_ALLOWED_KEYS,
  defaultSkillRoot,
  defaultWorkspaceRoot,
  getMetadataPaths,
  getWorkspaceMetadataPaths,
  readJsonFile,
  writeJsonFile,
  upsertProjectIndexEntry,
  readObservedMetadata,
  readWorkspaceObservedMetadata,
  readAuthoritativeMetadata,
  readWorkspaceAuthoritativeMetadata,
  writeObservedMetadata,
  writeWorkspaceObservedMetadata,
  resolveMetadataValue,
  resolveWorkspaceMetadataValue,
  markObservedMetadataStale,
  markWorkspaceObservedMetadataStale,
  invalidateObservedMetadata,
  invalidateWorkspaceObservedMetadata,
};
