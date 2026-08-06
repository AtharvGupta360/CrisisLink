import { useState, useCallback, useEffect, createContext, useContext } from 'react';
import { getToken, clearToken, decodeClaims } from './api.js';
import Login from './views/Login.jsx';
import Citizen from './views/Citizen.jsx';
import Responder from './views/Responder.jsx';
import ShelterMgr from './views/ShelterMgr.jsx';
import Operator from './views/Operator.jsx';

// --- toasts ------------------------------------------------------------------
// A tiny notification bus. The concurrency demo fires ten requests at once and
// needs to show ten independent results, which is exactly what this is for.
const ToastCtx = createContext(() => {});
export const useToast = () => useContext(ToastCtx);

function Toasts({ items }) {
  return (
    <div className="toasts">
      {items.map((t) => (
        <div key={t.id} className={`toast ${t.kind}`}>{t.text}</div>
      ))}
    </div>
  );
}

export default function App() {
  const [token, setTokenState] = useState(getToken());
  const [toasts, setToasts] = useState([]);

  const toast = useCallback((text, kind = 'ok') => {
    const id = Math.random().toString(36).slice(2);
    setToasts((t) => [...t, { id, text, kind }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 4200);
  }, []);

  const logout = useCallback(() => {
    clearToken();
    setTokenState(null);
  }, []);

  // The API layer fires this when any authenticated call comes back 401, which
  // means the token expired mid-session. Without it the UI keeps rendering a
  // logged-in shell that can no longer load anything.
  useEffect(() => {
    const onUnauthorized = () => {
      setTokenState(null);
      toast('Session expired. Please sign in again.', 'err');
    };
    window.addEventListener('crisislink:unauthorized', onUnauthorized);
    return () => window.removeEventListener('crisislink:unauthorized', onUnauthorized);
  }, [toast]);

  if (!token) {
    return (
      <ToastCtx.Provider value={toast}>
        <Login onAuth={setTokenState} />
        <Toasts items={toasts} />
      </ToastCtx.Provider>
    );
  }

  const claims = decodeClaims(token);
  if (!claims) {
    clearToken();
    return <Login onAuth={setTokenState} />;
  }

  // THE ROLE ROUTER. One app, one login, four completely different products —
  // which is the visible form of the server-side RBAC. Rendering a different
  // screen is a convenience, not a security boundary: every action below is
  // independently authorized by the API, so a tampered token changes the UI and
  // nothing else.
  const views = {
    citizen: Citizen,
    responder: Responder,
    shelter_manager: ShelterMgr,
    operator: Operator,
    admin: Operator, // admin sees the operator console plus admin-only panels
  };
  const View = views[claims.role] || Citizen;

  return (
    <ToastCtx.Provider value={toast}>
      <View claims={claims} onLogout={logout} />
      <Toasts items={toasts} />
    </ToastCtx.Provider>
  );
}

// Shared top bar. `extra` lets each view slot in its own live stats.
export function TopBar({ claims, onLogout, extra }) {
  return (
    <div className="topbar">
      <div className="brand">Crisis<span>Link</span></div>
      <span className="role-chip">{claims.role.replace('_', ' ')}</span>
      <div className="spacer" />
      {extra}
      <span className="stat dim">{claims.username}</span>
      <button onClick={onLogout}>Sign out</button>
    </div>
  );
}
