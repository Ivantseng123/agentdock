#!/usr/bin/env node

const { Buffer } = require('node:buffer');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const { getStatusPayload, requestJson, requireApiEnv } = require('./lib/api');
const {
  compactIssues,
  flattenIssueAttachments,
  reporterName,
  summarizeAssignmentStats,
  summarizeIssueStats,
} = require('./lib/normalizers');
const {
  CliTextError,
  loadDotenv,
  parseArgs,
  printJson,
  printText,
} = require('./lib/runtime');

const SCRIPT_DIR = __dirname;
const SKILL_DIR = path.resolve(SCRIPT_DIR, '..');

loadDotenv(SKILL_DIR);

function usageText() {
  return `用法: mantis.js <命令> [參數...]

狀態:
  status                                           檢查 API 連線狀態

問題操作:
  get-issue <id> [--full]                          取得問題詳情
  list-issues [選項]                               列出問題
    --project <id>  --filter <id>  [--status <id>]  --handler <id>  --reporter <id>
    --search <關鍵字>  --select <欄位,欄位,...>
    --page <N>  --page-size <N>  [--full]
  create-issue --project <id> --summary <摘要> --description <描述> [選項]
    --category <id>  --handler <id>  --priority <名稱>  --severity <名稱>
    --additional-info <文字>
  update-issue <id> [選項]
    --summary <摘要>  --description <描述>  --status <名稱>
    --resolution <名稱>  --priority <名稱>  --severity <名稱>  --handler <id>
  add-note <issue_id> --text <內容> [--private]

使用者操作:
  get-user <username>                              查詢使用者
  get-users                                        取得所有使用者
  get-project-users <project_id>                   取得專案使用者

專案操作:
  get-projects                                     列出所有專案

統計:
  issue-stats --group-by <status|priority|severity|handler|reporter> [--project <id>] [--period <all|today|week|month>]
  assignment-stats [--project <id>] [--status-filter <id,id,...>] [--no-unassigned]

附件操作:
  download-attachment <issue_id> <file_id> [--output <路徑>]  下載附件
  list-attachments <issue_id>                      列出問題的附件`;
}

function apiContext() {
  return {
    apiUrl: process.env.MANTIS_API_URL || '',
    apiToken: process.env.MANTIS_API_TOKEN || '',
  };
}

function buildIssuePath(id) {
  return `/issues/${encodeURIComponent(String(id))}`;
}

function requireNumericId(value, label) {
  if (typeof value !== 'string' || !/^\d+$/.test(value)) {
    throw new CliTextError(`參數 ${label} 必須是數字 ID`);
  }

  return value;
}

function numericIdValue(value, label) {
  return Number(requireNumericId(value, label));
}

async function cmdStatus() {
  return getStatusPayload(apiContext());
}

async function cmdGetIssue(args) {
  const parsed = parseArgs(args, { booleanFlags: ['--full'] });
  const [issueId] = parsed.positionals;
  if (!issueId) {
    throw new CliTextError('用法: get-issue <id> [--full]');
  }

  const data = await requestJson({ ...apiContext(), path: buildIssuePath(issueId) });
  return parsed.options.full ? data : compactIssues(data);
}

async function cmdListIssues(args) {
  const parsed = parseArgs(args, {
    booleanFlags: ['--full'],
    aliases: { '--status': '--filter' },
  });

  const params = new URLSearchParams();
  if (parsed.options.project) params.set('project_id', parsed.options.project);
  if (parsed.options.filter) params.set('filter_id', parsed.options.filter);
  if (parsed.options.handler) params.set('handler_id', parsed.options.handler);
  if (parsed.options.reporter) params.set('reporter_id', parsed.options.reporter);
  if (parsed.options.search) params.set('search', parsed.options.search);
  if (parsed.options.select) params.set('select', parsed.options.select);
  if (parsed.options.page) params.set('page', parsed.options.page);
  if (parsed.options['page-size']) params.set('page_size', parsed.options['page-size']);

  const query = params.toString();
  const data = await requestJson({
    ...apiContext(),
    path: `/issues${query ? `?${query}` : ''}`,
  });

  return parsed.options.full ? data : compactIssues(data);
}

async function cmdCreateIssue(args) {
  const parsed = parseArgs(args);
  const { project, summary, description, category, handler, priority, severity } = parsed.options;
  const additionalInfo = parsed.options['additional-info'];

  if (!project || !summary || !description) {
    throw new CliTextError('必要參數: --project, --summary, --description');
  }

  const payload = {
    project: { id: numericIdValue(project, '--project') },
    summary,
    description,
  };

  if (category) payload.category = { id: numericIdValue(category, '--category') };
  if (handler) payload.handler = { id: numericIdValue(handler, '--handler') };
  if (priority) payload.priority = { name: priority };
  if (severity) payload.severity = { name: severity };
  if (additionalInfo) payload.additional_information = additionalInfo;

  return requestJson({ ...apiContext(), path: '/issues', method: 'POST', body: payload });
}

async function cmdUpdateIssue(args) {
  const [issueId, ...rest] = args;
  if (!issueId) {
    throw new CliTextError('用法: update-issue <id> [選項]');
  }

  const numericIssueId = requireNumericId(issueId, '<id>');

  const parsed = parseArgs(rest);
  const payload = {};

  if (parsed.options.summary) payload.summary = parsed.options.summary;
  if (parsed.options.description) payload.description = parsed.options.description;
  if (parsed.options.status) payload.status = { name: parsed.options.status };
  if (parsed.options.resolution) payload.resolution = { name: parsed.options.resolution };
  if (parsed.options.priority) payload.priority = { name: parsed.options.priority };
  if (parsed.options.severity) payload.severity = { name: parsed.options.severity };
  if (parsed.options.handler) payload.handler = { id: numericIdValue(parsed.options.handler, '--handler') };

  return requestJson({
    ...apiContext(),
    path: buildIssuePath(numericIssueId),
    method: 'PATCH',
    body: payload,
  });
}

async function cmdAddNote(args) {
  const [issueId, ...rest] = args;
  if (!issueId) {
    throw new CliTextError('用法: add-note <issue_id> --text <內容> [--private]');
  }

  const numericIssueId = requireNumericId(issueId, '<issue_id>');

  const parsed = parseArgs(rest, { booleanFlags: ['--private'] });
  if (!parsed.options.text) {
    throw new CliTextError('必要參數: --text');
  }

  return requestJson({
    ...apiContext(),
    path: `${buildIssuePath(numericIssueId)}/notes`,
    method: 'POST',
    body: {
      text: parsed.options.text,
      view_state: { name: parsed.options.private ? 'private' : 'public' },
    },
  });
}

async function cmdGetUser(args) {
  const [username] = args;
  if (!username) {
    throw new CliTextError('用法: get-user <username>');
  }

  const data = await requestJson({ ...apiContext(), path: '/users' });
  const users = Array.isArray(data) ? data : data?.users || [];
  return users.find((user) => user?.name === username || user?.username === username) || {
    error: `User not found: ${username}`,
  };
}

async function cmdGetUsers() {
  return requestJson({ ...apiContext(), path: '/users' });
}

async function cmdGetProjectUsers(args) {
  const [projectId] = args;
  if (!projectId) {
    throw new CliTextError('用法: get-project-users <project_id>');
  }

  return requestJson({ ...apiContext(), path: `/projects/${requireNumericId(projectId, '<project_id>')}/users` });
}

async function cmdGetProjects() {
  return requestJson({ ...apiContext(), path: '/projects' });
}

async function cmdIssueStats(args) {
  const parsed = parseArgs(args);
  if (!parsed.options['group-by']) {
    throw new CliTextError('必要參數: --group-by');
  }

  const params = new URLSearchParams({ page_size: '150' });
  if (parsed.options.project) {
    params.set('project_id', parsed.options.project);
  }

  const data = await requestJson({
    ...apiContext(),
    path: `/issues?${params.toString()}`,
  });

  return summarizeIssueStats(data, {
    groupBy: parsed.options['group-by'],
    period: parsed.options.period || 'all',
  });
}

async function cmdAssignmentStats(args) {
  const parsed = parseArgs(args, { booleanFlags: ['--no-unassigned'] });

  const params = new URLSearchParams({ page_size: '150' });
  if (parsed.options.project) {
    params.set('project_id', parsed.options.project);
  }

  const data = await requestJson({
    ...apiContext(),
    path: `/issues?${params.toString()}`,
  });

  return summarizeAssignmentStats(data, {
    statusFilter: parsed.options['status-filter'] || '',
    includeUnassigned: !parsed.options['no-unassigned'],
  });
}

async function cmdListAttachments(args) {
  const [issueId] = args;
  if (!issueId) {
    throw new CliTextError('用法: list-attachments <issue_id>');
  }

  const data = await requestJson({ ...apiContext(), path: buildIssuePath(issueId) });
  return flattenIssueAttachments(data);
}

function normalizeFileResponse(data) {
  if (Array.isArray(data?.files)) {
    return data.files;
  }

  if (data) {
    return [data];
  }

  return [];
}

async function cmdDownloadAttachment(args) {
  if (args.length < 2) {
    throw new CliTextError('用法: download-attachment <issue_id> <file_id> [--output <路徑>]');
  }

  const [issueId, fileId, ...rest] = args;
  const validatedIssueId = requireNumericId(issueId, '<issue_id>');
  const validatedFileId = requireNumericId(fileId, '<file_id>');
  const parsed = parseArgs(rest);
  const data = await requestJson({
    ...apiContext(),
    path: `${buildIssuePath(validatedIssueId)}/files/${encodeURIComponent(validatedFileId)}`,
  });

  const files = normalizeFileResponse(data);
  if (files.length === 0) {
    throw new CliTextError(`找不到附件 file_id=${fileId} (issue=${issueId})`);
  }

  const file = files[0];
  const filename = file.filename || '';
  const outputPath = parsed.options.output || path.join(
      os.tmpdir(),
      filename ? `mantis_${validatedFileId}_${filename}` : `mantis_attachment_${validatedFileId}`,
    );

  const buffer = Buffer.from(file.content || '', 'base64');
  fs.writeFileSync(outputPath, buffer);

  return {
    status: 'success',
    file_id: Number(validatedFileId),
    filename: filename || 'unknown',
    path: outputPath,
    size: buffer.byteLength,
  };
}

const COMMANDS = {
  status: cmdStatus,
  'get-issue': cmdGetIssue,
  'list-issues': cmdListIssues,
  'create-issue': cmdCreateIssue,
  'update-issue': cmdUpdateIssue,
  'add-note': cmdAddNote,
  'get-user': cmdGetUser,
  'get-users': cmdGetUsers,
  'get-project-users': cmdGetProjectUsers,
  'get-projects': cmdGetProjects,
  'issue-stats': cmdIssueStats,
  'assignment-stats': cmdAssignmentStats,
  'list-attachments': cmdListAttachments,
  'download-attachment': cmdDownloadAttachment,
};

async function main(argv = process.argv.slice(2)) {
  const [command, ...args] = argv;

  if (!command) {
    throw new CliTextError(usageText(), { stream: 'stdout' });
  }

  if (['help', '--help', '-h'].includes(command)) {
    throw new CliTextError(usageText(), { stream: 'stdout' });
  }

  const handler = COMMANDS[command];
  if (!handler) {
    printText(`未知命令: ${command}`, 'stderr');
    throw new CliTextError(usageText(), { stream: 'stdout' });
  }

  if (command !== 'status') {
    requireApiEnv(apiContext());
  }

  const result = await handler(args);
  if (result === undefined || result === null) {
    return;
  }

  if (typeof result === 'string') {
    printText(result, 'stdout');
    return;
  }

  printJson(result);
}

if (require.main === module) {
  main().catch((error) => {
    if (error instanceof CliTextError) {
      printText(error.message, error.stream);
      process.exit(error.exitCode);
    }

    printText(error?.message || String(error), 'stderr');
    process.exit(1);
  });
}

module.exports = {
  COMMANDS,
  cmdStatus,
  main,
  normalizeFileResponse,
  reporterName,
  usageText,
};
