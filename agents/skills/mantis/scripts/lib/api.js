const { CliTextError } = require('./runtime');

const DEFAULT_TIMEOUT_MS = 10000;

class HttpError extends Error {
  constructor(message, { httpCode = 0, responseBody = '', cause = null } = {}) {
    super(message, cause ? { cause } : undefined);
    this.name = 'HttpError';
    this.httpCode = httpCode;
    this.responseBody = responseBody;
  }
}

function listMissingApiEnv({ apiUrl, apiToken }) {
  const missing = [];

  if (!apiUrl) {
    missing.push('MANTIS_API_URL');
  }

  if (!apiToken) {
    missing.push('MANTIS_API_TOKEN');
  }

  return missing;
}

function describeMissingApiEnv(missing) {
  if (!Array.isArray(missing) || missing.length === 0) {
    return '';
  }

  return `請設定 ${missing.join('、')}（可用環境變數或 .env）`;
}

function requireApiEnv({ apiUrl, apiToken }) {
  const missing = listMissingApiEnv({ apiUrl, apiToken });
  if (missing.length > 0) {
    throw new CliTextError(describeMissingApiEnv(missing));
  }
}

function buildHeaders(apiToken, hasJsonBody) {
  const headers = { Authorization: apiToken };
  if (hasJsonBody) {
    headers['Content-Type'] = 'application/json';
  }

  return headers;
}

function describeHttpFailure(method, url, responseText, status) {
  const suffix = responseText ? `: ${responseText}` : '';
  return `${method} ${url} 失敗（HTTP ${status}）${suffix}`;
}

async function requestJson({ apiUrl, apiToken, path, method = 'GET', body = undefined, timeoutMs = DEFAULT_TIMEOUT_MS }) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  const url = `${apiUrl.replace(/\/$/, '')}${path}`;

  try {
    const response = await fetch(url, {
      method,
      headers: buildHeaders(apiToken, body !== undefined),
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: controller.signal,
    });

    const responseText = await response.text();
    if (!response.ok) {
      throw new HttpError(describeHttpFailure(method, url, responseText, response.status), {
        httpCode: response.status,
        responseBody: responseText,
      });
    }

    if (!responseText) {
      return null;
    }

    return JSON.parse(responseText);
  } catch (error) {
    if (error instanceof HttpError) {
      throw error;
    }

    if (error?.name === 'AbortError') {
      throw new HttpError('Request timed out', { cause: error });
    }

    throw new HttpError(error?.message || 'Unknown request error', { cause: error });
  } finally {
    clearTimeout(timer);
  }
}

function extractErrorMessage(error) {
  if (error instanceof HttpError) {
    return error.responseBody || error.message || '';
  }

  return error?.message || String(error || '');
}

function collectErrorCodes(error) {
  return [error?.code, error?.cause?.code, error?.errno].filter(Boolean);
}

function mapFailureReason(error) {
  const httpCode = error?.httpCode ?? 0;
  const message = `${error?.message || ''} ${error?.responseBody || ''}`.toLowerCase();
  const codes = collectErrorCodes(error).map((code) => String(code).toUpperCase());

  if (httpCode === 401 || httpCode === 403) {
    return 'auth_failed';
  }

  if (
    codes.includes('EPERM') ||
    codes.includes('EACCES') ||
    codes.includes('ECONNREFUSED') ||
    codes.includes('ENETUNREACH') ||
    codes.includes('ECONNRESET') ||
    /permission denied|access permissions|operation not permitted|forbidden by its access permissions/.test(message)
  ) {
    return 'sandbox_network_blocked';
  }

  if (
    codes.includes('ENOTFOUND') ||
    codes.includes('EAI_AGAIN') ||
    /could not resolve|name or service not known|no such host is known|getaddrinfo enotfound/.test(message)
  ) {
    return 'dns_error';
  }

  if (
    codes.includes('CERT_HAS_EXPIRED') ||
    codes.includes('DEPTH_ZERO_SELF_SIGNED_CERT') ||
    codes.includes('UNABLE_TO_VERIFY_LEAF_SIGNATURE') ||
    codes.includes('ERR_TLS_CERT_ALTNAME_INVALID') ||
    /ssl|tls|certificate|cert /.test(message)
  ) {
    return 'tls_error';
  }

  if (codes.includes('ETIMEDOUT') || error?.name === 'AbortError' || /timed out|timeout/.test(message)) {
    return 'timeout';
  }

  return 'unknown';
}

async function getStatusPayload({ apiUrl, apiToken }) {
  const payload = {
    api: {
      status: 'false',
      url: apiUrl || '',
      reason: '',
      http_code: 0,
      message: '',
    },
  };

  const missing = listMissingApiEnv({ apiUrl, apiToken });
  if (missing.length > 0) {
    payload.api.message = describeMissingApiEnv(missing);
    return payload;
  }

  try {
    await requestJson({ apiUrl, apiToken, path: '/users/me' });
    payload.api.status = 'true';
    payload.api.http_code = 200;
    payload.api.message = '';
    return payload;
  } catch (error) {
    payload.api.reason = mapFailureReason(error);
    payload.api.http_code = error?.httpCode ?? 0;
    payload.api.message = extractErrorMessage(error);
    return payload;
  }
}

module.exports = {
  HttpError,
  describeMissingApiEnv,
  getStatusPayload,
  listMissingApiEnv,
  mapFailureReason,
  requestJson,
  requireApiEnv,
};
