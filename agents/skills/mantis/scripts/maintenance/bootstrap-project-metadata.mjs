#!/usr/bin/env node

import fs from 'node:fs';
import path from 'node:path';
import metadataStore from '../lib/metadata-store.js';
import { requireApiEnv, requestJson } from '../lib/api.js';

const {
  getMetadataPaths,
  readObservedMetadata,
  readJsonFile,
  writeJsonFile,
  upsertProjectIndexEntry,
  markObservedMetadataStale,
} = metadataStore;

function parseArgs(argv) {
  const projectIds = [];

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];

    if (arg === '--project') {
      const value = argv[index + 1];
      if (!value || value.startsWith('--')) {
        throw new Error('Missing value after --project');
      }
      if (!/^[0-9]+$/.test(value)) {
        throw new Error(`Invalid project id for --project: ${value}`);
      }
      projectIds.push(value);
      index += 1;
      continue;
    }

    throw new Error(`Unknown argument: ${arg}`);
  }

  if (projectIds.length === 0) {
    throw new Error('Use --project <id>');
  }

  return { projectIds: [...new Set(projectIds)] };
}

function nowIso() {
  return process.env.MANTIS_BOOTSTRAP_NOW || process.env.MANTIS_REFRESH_NOW || new Date().toISOString();
}

function createPayloadState() {
  return {
    changed: { index: { updated: false }, projects: [], observed: [] },
    verified: { index: { exists: false, parseable: false }, projects: [], observed: [] },
    risk: [],
  };
}

function resolveSkillRoot() {
  const explicit = process.env.MANTIS_SKILL_ROOT || process.env.MANTIS_WORKSPACE_ROOT;
  return explicit ? path.resolve(explicit) : undefined;
}

function getApiContext() {
  return {
    apiUrl: process.env.MANTIS_API_URL || '',
    apiToken: process.env.MANTIS_API_TOKEN || '',
  };
}

function extractProjectFromPayload(payload, projectId) {
  if (!payload || typeof payload !== 'object') {
    return null;
  }

  if (Array.isArray(payload.projects)) {
    return payload.projects.find((candidate) => String(candidate?.id) === String(projectId)) || null;
  }

  if (payload.id !== undefined) {
    return String(payload.id) === String(projectId) ? payload : null;
  }

  if (payload.project && payload.project.id !== undefined) {
    return String(payload.project.id) === String(projectId) ? payload.project : null;
  }

  return null;
}

function ensureCategoriesShape(rawCategories) {
  if (Array.isArray(rawCategories)) {
    return { all: rawCategories };
  }

  if (rawCategories && Array.isArray(rawCategories.all)) {
    return { all: rawCategories.all };
  }

  return null;
}

function parsePipeValues(raw = '') {
  if (typeof raw !== 'string' || raw.trim() === '') {
    return {
      raw,
      exact: [],
      display: [],
    };
  }

  const exact = String(raw).split('|');
  return {
    raw: String(raw),
    exact,
    display: exact.map((value) => (value === ' ' ? '' : value)),
  };
}

function readIndexStatus({ indexPath }) {
  try {
    const doc = readJsonFile(indexPath);
    if (doc === null) {
      return {
        exists: false,
        parseable: false,
        doc: null,
      };
    }

    return {
      exists: true,
      parseable: typeof doc === 'object' && doc !== null,
      doc,
    };
  } catch (error) {
    return {
      exists: true,
      parseable: false,
      doc: null,
      message: error.message,
    };
  }
}

function readObservedStatus({ skillRoot, projectId }) {
  try {
    const observedDoc = readObservedMetadata({ skillRoot, projectId });
    return {
      status: observedDoc ? 'present' : 'missing',
    };
  } catch (error) {
    return {
      status: 'parse_error',
      parseable: false,
      message: error.message,
    };
  }
}

function readFileSnapshot(filePath) {
  try {
    return {
      exists: true,
      content: fs.readFileSync(filePath, 'utf8'),
    };
  } catch (error) {
    if (error.code === 'ENOENT') {
      return {
        exists: false,
      };
    }

    throw error;
  }
}

function restoreFileFromSnapshot(filePath, snapshot) {
  if (snapshot.exists) {
    fs.mkdirSync(path.dirname(filePath), { recursive: true });
    fs.writeFileSync(filePath, snapshot.content, 'utf8');
    return;
  }

  if (fs.existsSync(filePath)) {
    fs.unlinkSync(filePath);
  }
}

function rollbackWrites(appliedWrites, snapshots, risk) {
  for (const filePath of appliedWrites) {
    const snapshot = snapshots.get(filePath);
    if (!snapshot) {
      continue;
    }

    try {
      restoreFileFromSnapshot(filePath, snapshot);
    } catch (error) {
      risk.push(`metadata rollback 失敗: ${filePath}: ${error.message}`);
    }
  }
}

function normalizeField(field, index) {
  if (!field || typeof field !== 'object') {
    throw new Error(`Custom field #${index} is invalid`);
  }

  const fieldId = Number(field.id);
  if (!Number.isInteger(fieldId) || fieldId < 0) {
    throw new Error(`Custom field #${index} must have a valid id`);
  }

  return {
    id: fieldId,
    name: field.name || '',
    type: field.type || '',
    required: {
      report: Boolean(field.require_report),
      update: Boolean(field.require_update),
      resolved: Boolean(field.require_resolved),
      closed: Boolean(field.require_closed),
    },
    display: {
      report: Boolean(field.display_report),
      update: Boolean(field.display_update),
      resolved: Boolean(field.display_resolved),
      closed: Boolean(field.display_closed),
    },
    values: parsePipeValues(field.possible_values || ''),
  };
}

function normalizeFields(customFields) {
  if (!Array.isArray(customFields)) {
    throw new Error('custom_fields must be an array');
  }

  const all = customFields.map((field, index) => normalizeField(field, index));
  const requiredOnCreate = all.filter((field) => field.required.report).map((field) => field.id);

  return {
    required_on_create: requiredOnCreate,
    all,
  };
}

function buildAuthoritativeProjectSnapshot(project, refreshedAt) {
  const normalizedProject = {
    id: Number(project.id),
    name: project.name || '',
    last_refreshed_at: refreshedAt,
  };

  if (!Number.isInteger(normalizedProject.id) || normalizedProject.id < 0) {
    throw new Error(`Invalid project id: ${project.id}`);
  }

  const categories = ensureCategoriesShape(project.categories);
  if (!categories) {
    throw new Error(`Project ${project.id} 缺少 categories`);
  }

  const fields = normalizeFields(project.custom_fields);

  return {
    schema_version: 1,
    project: normalizedProject,
    categories,
    fields,
    known_quirks: [],
    validated_examples: {},
  };
}

async function fetchSingleProject(apiContext, projectId) {
  const singlePayload = await requestJson({ ...apiContext, path: `/projects/${encodeURIComponent(projectId)}` });
  const singleProject = extractProjectFromPayload(singlePayload, projectId);
  if (!singleProject) {
    throw new Error(`找不到 project ${projectId}`);
  }

  if (!Object.hasOwn(singleProject, 'categories')) {
    const allPayload = await requestJson({ ...apiContext, path: '/projects/' });
    const allProjects = allPayload?.projects;
    if (!Array.isArray(allProjects)) {
      throw new Error('GET /api/rest/projects/ 回應格式不符');
    }

    const fallbackProject = allProjects.find((candidate) => String(candidate?.id) === String(projectId));
    if (!fallbackProject) {
      throw new Error(`fallback 找不到 project ${projectId}`);
    }

    if (!Object.hasOwn(fallbackProject, 'categories')) {
      throw new Error(`fallback 的 project ${projectId} 仍缺 categories`);
    }

    singleProject.categories = fallbackProject.categories;
  }

  return singleProject;
}

async function main() {
  const state = createPayloadState();
  const changed = state.changed;
  const verified = state.verified;
  const risk = state.risk;

  try {
    const args = parseArgs(process.argv.slice(2));
    const apiContext = getApiContext();
    requireApiEnv(apiContext);

    const skillRoot = resolveSkillRoot();
    const timestamp = nowIso();
    const paths = getMetadataPaths({ projectId: args.projectIds[0] || 0, skillRoot });

    const indexStatus = readIndexStatus({ indexPath: paths.indexPath });
    if (indexStatus.message) {
      risk.push(`metadata/index.json 解析失敗: ${indexStatus.message}`);
    }

    verified.index.exists = indexStatus.exists;
    verified.index.parseable = indexStatus.parseable;

    const indexDoc = indexStatus.doc && typeof indexStatus.doc === 'object' && !Array.isArray(indexStatus.doc)
      ? indexStatus.doc
      : { projects: [] };

    const projectTargets = args.projectIds;
    const writes = [];
    const observedTargets = [];
    let indexUpdated = false;

    for (const projectId of projectTargets) {
      const projectPaths = getMetadataPaths({ skillRoot, projectId });

      const verifiedProject = { project_id: Number(projectId), exists: true, parseable: false };
      verified.projects.push(verifiedProject);

      const observedReadResult = readObservedStatus({ skillRoot, projectId });
      verified.observed.push({ project_id: Number(projectId), status: observedReadResult.status });
      if (observedReadResult.parseable === false) {
        risk.push(`metadata/observed/${projectId}.json 解析失敗: ${observedReadResult.message}`);
      }

      const rawProject = await fetchSingleProject(apiContext, projectId);
      const projectDoc = buildAuthoritativeProjectSnapshot(rawProject, timestamp);

      verifiedProject.parseable = true;

      writes.push({
        path: projectPaths.projectPath,
        value: projectDoc,
        observedPath: projectPaths.observedPath,
        projectId,
      });

      observedTargets.push({ projectId, observedPath: projectPaths.observedPath });

      const upsertedIndex = upsertProjectIndexEntry(indexDoc, {
        projectId,
        refreshedAt: timestamp,
      });
      if (upsertedIndex.changed) {
        indexUpdated = true;
      }
    }

    if (risk.length === 0) {
      let snapshots = new Map();
      let appliedWrites = [];

      try {
        snapshots = new Map();
        appliedWrites = [];

        for (const write of writes) {
          snapshots.set(write.path, readFileSnapshot(write.path));
        }

        for (const target of observedTargets) {
          snapshots.set(target.observedPath, readFileSnapshot(target.observedPath));
        }

        snapshots.set(paths.indexPath, readFileSnapshot(paths.indexPath));

        const observedWriteResults = [];

        for (const write of writes) {
          writeJsonFile(write.path, write.value);
          appliedWrites.push(write.path);
          changed.projects.push({ project_id: Number(write.projectId), status: 'written' });
        }

        writeJsonFile(paths.indexPath, indexDoc);
        appliedWrites.push(paths.indexPath);

        verified.index.exists = true;
        verified.index.parseable = true;
        changed.index.updated = indexUpdated;

        for (const target of args.projectIds) {
          const staleResult = markObservedMetadataStale({
            skillRoot,
            projectId: target,
            staleAt: timestamp,
            reason: 'bootstrap',
          });

          const status = staleResult ? 'stale' : 'missing';
          observedWriteResults.push({
            projectId: Number(target),
            status,
            observedPath: getMetadataPaths({ skillRoot, projectId: target }).observedPath,
            wasUpdated: Boolean(staleResult),
          });
        }

        for (const observedWrite of observedWriteResults) {
          changed.observed.push({ project_id: observedWrite.projectId, status: observedWrite.status });

          const verifiedObserved = verified.observed.find((entry) => entry.project_id === observedWrite.projectId);
          if (verifiedObserved) {
            verifiedObserved.status = observedWrite.status;
          }

          if (observedWrite.wasUpdated) {
            appliedWrites.push(observedWrite.observedPath);
          }
        }
      } catch (error) {
        risk.push(`metadata 寫入失敗: ${error.message}`);

        rollbackWrites(new Set(appliedWrites), snapshots, risk);

        changed.projects.length = 0;
        changed.observed.length = 0;
        changed.index.updated = false;

        verified.index.exists = indexStatus.exists;
        verified.index.parseable = indexStatus.parseable;
        verified.observed = verified.observed.map((entry) => {
          const observedReadResult = readObservedStatus({ skillRoot, projectId: entry.project_id });
          return {
            project_id: entry.project_id,
            status: observedReadResult.status,
          };
        });
      }
    }
  } catch (error) {
    risk.push(error.message);
  }

  const payload = {
    changed,
    verified,
    risk,
  };

  process.stdout.write(`${JSON.stringify(payload, null, 2)}\n`);
  process.exit(risk.length === 0 ? 0 : 1);
}

try {
  await main();
} catch (error) {
  const payload = createPayloadState();
  payload.risk.push(error.message);
  process.stdout.write(`${JSON.stringify(payload, null, 2)}\n`);
  process.exit(1);
}
