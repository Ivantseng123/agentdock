function reporterName(entity) {
  const reporter = entity?.reporter ?? entity ?? {};
  return reporter.real_name || reporter.name || '';
}

function normalizeIssuesInput(data) {
  if (Array.isArray(data)) {
    return data;
  }

  if (data && typeof data === 'object') {
    if (Array.isArray(data.issues)) {
      return data.issues;
    }

    return [data];
  }

  return [];
}

function compactIssue(issue) {
  const compact = {
    id: issue?.id,
    summary: issue?.summary ?? '',
    description: issue?.description ?? '',
    project: issue?.project?.name ?? '',
    category: issue?.category?.name ?? '',
    reporter: reporterName(issue?.reporter),
    handler: reporterName(issue?.handler),
    status: issue?.status?.label ?? '',
    resolution: issue?.resolution?.label ?? '',
    priority: issue?.priority?.label ?? '',
    severity: issue?.severity?.label ?? '',
    created_at: issue?.created_at ?? '',
    updated_at: issue?.updated_at ?? '',
  };

  const attachments = Array.isArray(issue?.attachments) ? issue.attachments : [];
  if (attachments.length > 0) {
    compact.attachments = attachments.map((attachment) => ({
      id: attachment.id,
      filename: attachment.filename,
      size: attachment.size ?? 0,
    }));
  }

  const notes = Array.isArray(issue?.notes) ? issue.notes : [];
  if (notes.length > 0) {
    compact.notes = notes.map((note) => ({
      id: note.id,
      reporter: reporterName(note?.reporter),
      text: note.text,
      created_at: note.created_at,
    }));
  }

  const customFields = Array.isArray(issue?.custom_fields) ? issue.custom_fields : [];
  if (customFields.length > 0) {
    compact.custom_fields = Object.fromEntries(
      customFields.map((field) => [field?.field?.name, field?.value]),
    );
  }

  return compact;
}

function compactIssues(data) {
  const issues = normalizeIssuesInput(data).map(compactIssue);
  return issues.length === 1 ? issues[0] : { issues };
}

function normalizeAttachment(attachment, source, note = null) {
  return {
    id: attachment.id,
    filename: attachment.filename,
    size: attachment.size ?? 0,
    content_type: attachment.content_type ?? '',
    created_at: attachment.created_at ?? '',
    reporter: reporterName(attachment),
    source,
    note_id: note?.id ?? null,
    note_created_at: note?.created_at ?? null,
  };
}

function flattenIssueAttachments(data) {
  const result = [];

  for (const issue of normalizeIssuesInput(data)) {
    const issueAttachments = Array.isArray(issue?.attachments) ? issue.attachments : [];
    for (const attachment of issueAttachments) {
      result.push(normalizeAttachment(attachment, 'issue'));
    }

    const notes = Array.isArray(issue?.notes) ? issue.notes : [];
    for (const note of notes) {
      const noteAttachments = Array.isArray(note?.attachments) ? note.attachments : [];
      for (const attachment of noteAttachments) {
        result.push(normalizeAttachment(attachment, 'note', note));
      }
    }
  }

  return result;
}

function toValidDate(value) {
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
}

function beginningOfToday(now) {
  return new Date(now.getFullYear(), now.getMonth(), now.getDate());
}

function beginningOfWeek(now) {
  const today = beginningOfToday(now);
  const dayOffset = (today.getDay() + 6) % 7;
  today.setDate(today.getDate() - dayOffset);
  return today;
}

function beginningOfMonth(now) {
  return new Date(now.getFullYear(), now.getMonth(), 1);
}

function filterIssuesByPeriod(issues, period) {
  const now = new Date();
  const cutoff = {
    today: beginningOfToday(now),
    week: beginningOfWeek(now),
    month: beginningOfMonth(now),
  }[period] ?? null;

  if (!cutoff) {
    return issues;
  }

  return issues.filter((issue) => {
    const createdAt = toValidDate(issue?.created_at);
    return !createdAt || createdAt >= cutoff;
  });
}

function summarizeIssueStats(data, { groupBy, period = 'all' }) {
  const filtered = filterIssuesByPeriod(normalizeIssuesInput(data), period);
  const counts = new Map();

  for (const issue of filtered) {
    let key = 'unknown';

    if (['status', 'priority', 'severity', 'resolution'].includes(groupBy)) {
      const field = issue?.[groupBy] ?? {};
      key = field.label || field.name || 'unknown';
    } else if (groupBy === 'handler') {
      key = reporterName(issue?.handler) || '未分派';
    } else if (groupBy === 'reporter') {
      key = reporterName(issue?.reporter) || 'unknown';
    }

    counts.set(key, (counts.get(key) ?? 0) + 1);
  }

  const groups = Object.fromEntries(
    [...counts.entries()].sort((left, right) => right[1] - left[1]),
  );

  return {
    group_by: groupBy,
    period,
    total: filtered.length,
    groups,
  };
}

function summarizeAssignmentStats(data, { statusFilter = '', includeUnassigned = true }) {
  const allowedStatusIds = statusFilter
    ? new Set(String(statusFilter).split(',').map((value) => value.trim()).filter(Boolean))
    : null;

  const filtered = normalizeIssuesInput(data).filter((issue) => {
    if (!allowedStatusIds) {
      return true;
    }

    return allowedStatusIds.has(String(issue?.status?.id ?? ''));
  });

  const assignments = new Map();
  for (const issue of filtered) {
    let assignee = reporterName(issue?.handler);
    if (!assignee) {
      if (!includeUnassigned) {
        continue;
      }

      assignee = '未分派';
    }

    const statusLabel = issue?.status?.label || issue?.status?.name || 'unknown';
    if (!assignments.has(assignee)) {
      assignments.set(assignee, { total: 0, by_status: {} });
    }

    const current = assignments.get(assignee);
    current.total += 1;
    current.by_status[statusLabel] = (current.by_status[statusLabel] ?? 0) + 1;
  }

  const orderedAssignments = Object.fromEntries(
    [...assignments.entries()]
      .sort((left, right) => right[1].total - left[1].total)
      .map(([name, stats]) => [name, stats]),
  );

  return {
    total_issues: filtered.length,
    assignments: orderedAssignments,
  };
}

module.exports = {
  compactIssues,
  flattenIssueAttachments,
  normalizeIssuesInput,
  reporterName,
  summarizeAssignmentStats,
  summarizeIssueStats,
};
