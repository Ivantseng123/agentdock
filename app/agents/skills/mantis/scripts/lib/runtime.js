const fs = require('node:fs');
const path = require('node:path');

class CliTextError extends Error {
  constructor(message, { exitCode = 1, stream = 'stderr' } = {}) {
    super(message);
    this.name = 'CliTextError';
    this.exitCode = exitCode;
    this.stream = stream;
  }
}

function trim(value) {
  return typeof value === 'string' ? value.trim() : value;
}

function unquote(value) {
  if (typeof value !== 'string' || value.length < 2) {
    return value;
  }

  if (value.startsWith('"') && value.endsWith('"')) {
    return value.slice(1, -1).replace(/\\"/g, '"').replace(/\\\\/g, '\\');
  }

  if (value.startsWith("'") && value.endsWith("'")) {
    return value.slice(1, -1);
  }

  return value;
}

function loadDotenv(skillDir) {
  const dotenvPath = path.join(skillDir, '.env');
  if (!fs.existsSync(dotenvPath)) {
    return;
  }

  const content = fs.readFileSync(dotenvPath, 'utf8');
  for (const rawLine of content.split(/\r?\n/)) {
    const line = trim(rawLine);
    if (!line || line.startsWith('#')) {
      continue;
    }

    const match = line.match(/^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/);
    if (!match) {
      continue;
    }

    const [, key, rawValue] = match;
    if (process.env[key]) {
      continue;
    }

    process.env[key] = unquote(trim(rawValue));
  }
}

function parseArgs(args, { booleanFlags = [], aliases = {} } = {}) {
  const result = { options: {}, positionals: [] };
  const flagSet = new Set(booleanFlags);

  for (let index = 0; index < args.length; index += 1) {
    const originalToken = args[index];
    const token = aliases[originalToken] || originalToken;

    if (flagSet.has(token)) {
      result.options[token.slice(2)] = true;
      continue;
    }

    if (token.startsWith('--')) {
      const value = args[index + 1];
      if (value == null) {
        throw new CliTextError(`參數 ${originalToken} 需要值`);
      }

      result.options[token.slice(2)] = value;
      index += 1;
      continue;
    }

    result.positionals.push(originalToken);
  }

  return result;
}

function printJson(value) {
  process.stdout.write(`${JSON.stringify(value, null, 2)}\n`);
}

function printText(message, stream = 'stdout') {
  const target = stream === 'stderr' ? process.stderr : process.stdout;
  target.write(message.endsWith('\n') ? message : `${message}\n`);
}

module.exports = {
  CliTextError,
  loadDotenv,
  parseArgs,
  printJson,
  printText,
};
