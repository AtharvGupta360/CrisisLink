// One place that knows how to talk to the Go API. Every response uses the same
// envelope — { success, message, data } — so unwrapping happens here once instead
// of in every component.

const BASE = import.meta.env.VITE_API_BASE || 'http://localhost:8080';
const API = `${BASE}/api/v1`;

// How often the live views re-poll. Deliberately slower in production: the demo
// runs on a free shared-CPU instance behind a 10 req/s edge rate limit, and every
// open tab costs ~1 req/s. Locally, 4s makes units visibly move while you watch.
export const POLL_MS = import.meta.env.PROD ? 10000 : 4000;

const TOKEN_KEY = 'crisislink.token';

export const getToken = () => localStorage.getItem(TOKEN_KEY);
export const setToken = (t) => localStorage.setItem(TOKEN_KEY, t);
export const clearToken = () => localStorage.removeItem(TOKEN_KEY);

// decodeClaims reads the JWT payload WITHOUT verifying it. That is fine and
// deliberate: the frontend uses claims only to decide which screen to render.
// Every real authorization decision is made by the server, which verifies the
// signature. A user who tampers with their token client-side just gets a UI that
// renders buttons the API will reject with 403.
export function decodeClaims(token) {
  if (!token) return null;
  try {
    let part = token.split('.')[1];
    if (!part) return null;
    part = part.replace(/-/g, '+').replace(/_/g, '/');
    // JWT payloads are base64URL and carry NO padding, but atob() is specified to
    // reject input whose length % 4 === 1 and is inconsistent across engines for
    // the other unpadded cases. Re-pad before decoding or the whole app can fail
    // to log in on a token whose length happens to land wrong.
    part += '='.repeat((4 - (part.length % 4)) % 4);
    return JSON.parse(atob(part));
  } catch {
    return null;
  }
}

export class ApiError extends Error {
  constructor(status, message, code) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function request(method, path, body) {
  const headers = { 'Content-Type': 'application/json' };
  const token = getToken();
  if (token) headers.Authorization = `Bearer ${token}`;

  const res = await fetch(`${API}${path}`, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  });

  // 204 and empty bodies are valid responses; don't try to parse them.
  const text = await res.text();
  const payload = text ? JSON.parse(text) : {};

  if (!res.ok) {
    // An expired or invalid token must not leave the app stuck showing error
    // toasts forever: drop the credential and bounce to the login screen. Tokens
    // are 24h, so this WILL happen to anyone who leaves a tab open overnight.
    // Excludes the login call itself, where 401 just means "wrong password".
    if (res.status === 401 && !path.startsWith('/auth/')) {
      clearToken();
      window.dispatchEvent(new Event('crisislink:unauthorized'));
    }
    // A 409 is not a bug — it is the system correctly refusing a race it lost.
    // Callers distinguish on status, so the status is carried on the error.
    throw new ApiError(res.status, payload.message || res.statusText, payload.error);
  }
  return payload.data;
}

export const api = {
  get: (p) => request('GET', p),
  post: (p, b) => request('POST', p, b),
  patch: (p, b) => request('PATCH', p, b),

  // --- auth ---
  // NOTE: register takes a username AND email; login takes EMAIL only.
  register: (username, email, password) =>
    request('POST', '/auth/register', { username, email, password }),
  login: (email, password) => request('POST', '/auth/login', { email, password }),

  // --- incidents ---
  listIncidents: (limit = 50) => request('GET', `/incidents?limit=${limit}`),
  getIncident: (id) => request('GET', `/incidents/${id}`),
  reportIncident: (b) => request('POST', '/incidents', b),
  candidates: (id) => request('GET', `/incidents/${id}/candidates`),
  dispatch: (id, unitId) => request('POST', `/incidents/${id}/dispatch`, { unitId }),
  preemptable: (id) => request('GET', `/incidents/${id}/preemptable`),
  preempt: (id) => request('POST', `/incidents/${id}/preempt`),
  incidentDispatches: (id) => request('GET', `/incidents/${id}/dispatches`),

  // --- units & presence ---
  listUnits: (status) => request('GET', `/units${status ? `?status=${status}` : ''}`),
  liveNearby: (lat, lng, radius = 50000) =>
    request('GET', `/units/nearby?lat=${lat}&lng=${lng}&radius=${radius}`),
  presence: (unitId) => request('GET', `/units/${unitId}/presence`),
  heartbeat: (unitId, latitude, longitude) =>
    request('POST', `/units/${unitId}/heartbeat`, { latitude, longitude }),

  // --- dispatches ---
  getDispatch: (id) => request('GET', `/dispatches/${id}`),
  advanceDispatch: (id, status) => request('PATCH', `/dispatches/${id}/status`, { status }),
  reroute: (id) => request('POST', `/dispatches/${id}/reroute`),

  // --- shelters & victims ---
  listShelters: () => request('GET', '/shelters'),
  setShelterStatus: (id, status) => request('PATCH', `/shelters/${id}/status`, { status }),
  listVictims: () => request('GET', '/victims'),
  assignVictim: (id, shelterId) => request('POST', `/victims/${id}/assign`, { shelterId }),
  nearestShelters: (victimId) => request('GET', `/victims/${victimId}/shelters`),

  // --- admin/ops ---
  outbox: () => request('GET', '/admin/outbox'),
  audit: () => request('GET', '/admin/audit'),
};

// Distance/ETA formatters shared by every view.
export const fmtMeters = (m) =>
  m == null ? '-' : m < 1000 ? `${Math.round(m)} m` : `${(m / 1000).toFixed(1)} km`;

export const fmtEta = (s) => {
  if (s == null) return '-';
  const mins = Math.floor(s / 60);
  const secs = Math.round(s % 60);
  return mins > 0 ? `${mins}m ${secs}s` : `${secs}s`;
};

export const fmtAgo = (iso) => {
  if (!iso) return '-';
  const diff = (Date.now() - new Date(iso).getTime()) / 1000;
  if (diff < 60) return `${Math.round(diff)}s ago`;
  if (diff < 3600) return `${Math.round(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.round(diff / 3600)}h ago`;
  return `${Math.round(diff / 86400)}d ago`;
};
