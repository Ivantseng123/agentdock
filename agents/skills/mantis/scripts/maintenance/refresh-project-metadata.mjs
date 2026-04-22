#!/usr/bin/env node

import fs from 'node:fs';
import path from 'node:path';
import metadataStore from '../lib/metadata-store.js';

const {
  getMetadataPaths,
  readJsonFile,
  writeJsonFile,
  readObservedMetadata,
  markObservedMetadataStale,
  upsertProjectIndexEntry,
} = metadataStore;

function parseArgs(argv) {
  const projectIds = [];
  let all = false;

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];

    if (arg === '--all') {
      all = true;
      continue;
    }

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

  if (!all && projectIds.length === 0) {
    throw new Error('Use --project <id> or --all');
  }

  return { all, projectIds };
}

function nowIso() {
  return process.env.MANTIS_REFRESH_NOW || new Date().toISOString();
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

function ensureProjectTargets(indexDoc, requestedIds) {
  const knownProjects = Array.isArray(indexDoc?.projects) ? indexDoc.projects : [];
  return requestedIds.map((projectId) => {
    const existing = knownProjects.find((entry) => String(entry.id) === String(projectId));
    if (existing) {
      return existing;
    }

    return {
      id: Number(projectId),
      path: `projects/${projectId}.json`,
      last_refreshed_at: '',
    };
  });
}

function parseIndexDocument({ indexPath }) {
  try {
    const doc = readJsonFile(indexPath);
    if (doc === null) {
      return {
        doc: null,
        exists: false,
        parseable: false,
      };
    }

    return {
      doc,
      exists: true,
      parseable: typeof doc === 'object' && doc !== null,
    };
  } catch (error) {
    return {
      doc: null,
      exists: true,
      parseable: false,
      message: error.message,
    };
  }
}

function readObservedStatus({ skillRoot, projectId }) {
  try {
    const observedDoc = readObservedMetadata({ skillRoot, projectId });
    return { status: observedDoc ? 'present' : 'missing' };
  } catch (error) {
    return { status: 'parse_error', parseable: false, message: error.message };
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

function main() {
  const state = createPayloadState();
  const changed = state.changed;
  const verified = state.verified;
  const risk = state.risk;

  try {
    const args = parseArgs(process.argv.slice(2));
    const skillRoot = resolveSkillRoot();
    const paths = getMetadataPaths({ skillRoot, projectId: args.projectIds[0] || 0 });
    const timestamp = nowIso();

    let indexDoc = null;
    const writes = [];
    let indexUpdated = false;

    const indexStatus = parseIndexDocument({ indexPath: paths.indexPath });
    if (indexStatus.message) {
      risk.push(`metadata/index.json 解析失敗: ${indexStatus.message}`);
    }

    verified.index.exists = indexStatus.exists;
    verified.index.parseable = indexStatus.parseable;

    if (!indexStatus.parseable && indexStatus.message) {
      indexDoc = null;
    } else {
      indexDoc = indexStatus.doc || { projects: [] };
    }

    indexDoc =
      indexDoc && typeof indexDoc === 'object' && !Array.isArray(indexDoc)
        ? indexDoc
        : { projects: [] };

    const safeIndexDoc = indexDoc && typeof indexDoc === 'object' ? indexDoc : { projects: [] };
    const targets = args.all
      ? ensureProjectTargets(safeIndexDoc, (Array.isArray(safeIndexDoc.projects) ? safeIndexDoc.projects : []).map((entry) => String(entry.id)))
      : ensureProjectTargets(safeIndexDoc, args.projectIds);

    indexDoc = safeIndexDoc;

    const writeTargets = [];
    const observedTargets = [];

    for (const target of targets) {
      const projectPaths = getMetadataPaths({ skillRoot, projectId: target.id });
      const verifiedProject = { project_id: Number(target.id), exists: true, parseable: false };
      const verifiedObserved = {
        project_id: Number(target.id),
        status: 'missing',
      };

      verified.observed.push(verifiedObserved);
      const observedReadResult = readObservedStatus({ skillRoot, projectId: target.id });
      verifiedObserved.status = observedReadResult.status;
      if (observedReadResult.parseable === false) {
        risk.push(`metadata/observed/${target.id}.json 解析失敗: ${observedReadResult.message}`);
      }

      let projectDoc;
      try {
        projectDoc = readJsonFile(projectPaths.projectPath);
      } catch (error) {
        risk.push(`metadata/projects/${target.id}.json 解析失敗: ${error.message}`);
        verifiedProject.parseable = false;
        verified.projects.push(verifiedProject);
        continue;
      }

      if (!projectDoc) {
        risk.push(`Missing project metadata: ${projectPaths.projectPath}`);
        verifiedProject.exists = false;
        verified.projects.push(verifiedProject);
        continue;
      }

      verifiedProject.parseable = true;
      verified.projects.push(verifiedProject);

      if (projectDoc.project && typeof projectDoc.project === 'object') {
        projectDoc.project.last_refreshed_at = timestamp;
      }

      writes.push({
        path: projectPaths.projectPath,
        value: projectDoc,
        projectId: target.id,
        observedPath: projectPaths.observedPath,
      });

      writeTargets.push(projectPaths.projectPath);
      observedTargets.push(projectPaths.observedPath);

      const upsertedIndex = upsertProjectIndexEntry(indexDoc, {
        projectId: target.id,
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

        for (const observedPath of observedTargets) {
          snapshots.set(observedPath, readFileSnapshot(observedPath));
        }

        snapshots.set(paths.indexPath, readFileSnapshot(paths.indexPath));

        for (const write of writes) {
          writeJsonFile(write.path, write.value);
          appliedWrites.push(write.path);
          changed.projects.push({ project_id: Number(write.projectId), authoritative_refreshed_at: timestamp });
        }

        writeJsonFile(paths.indexPath, indexDoc);
        appliedWrites.push(paths.indexPath);
        changed.index.updated = indexUpdated;
        verified.index.exists = true;
        verified.index.parseable = true;

        for (const target of writes) {
          const staleDoc = markObservedMetadataStale({
            skillRoot,
            projectId: target.projectId,
            staleAt: timestamp,
            reason: 'refresh',
          });

          const observedStatus = staleDoc ? 'stale' : 'missing';
          changed.observed.push({ project_id: Number(target.projectId), status: observedStatus });

          const verifiedObserved = verified.observed.find((entry) => entry.project_id === Number(target.projectId));
          if (verifiedObserved) {
            verifiedObserved.status = observedStatus;
          }

          if (staleDoc) {
            appliedWrites.push(target.observedPath);
          }
        }

        // no rollback needed on success
      } catch (error) {
        risk.push(`metadata 寫入失敗: ${error.message}`);
        rollbackWrites(new Set(appliedWrites), snapshots, risk);

        // undo optimistic payload changes
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
  main();
} catch (error) {
  const payload = createPayloadState();
  payload.risk.push(error.message);
  process.stdout.write(`${JSON.stringify(payload, null, 2)}\n`);
  process.exit(1);
}
