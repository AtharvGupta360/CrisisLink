import { useState } from 'react';
import { api, setToken } from '../api.js';

// The four seeded demo accounts. They exist so an interviewer can click through
// every role in ten seconds and watch the same API present four different
// systems — which is the RBAC story told without narration.
const DEMOS = [
  { label: 'Citizen', email: 'citizen@crisislink.dev' },
  { label: 'Responder', email: 'responder@crisislink.dev' },
  { label: 'Shelter mgr', email: 'shelter@crisislink.dev' },
  { label: 'Operator', email: 'operator@crisislink.dev' },
];

const DEMO_PASSWORD = 'password123';

export default function Login({ onAuth }) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  async function signIn(e, demoEmail) {
    e?.preventDefault();
    setErr('');
    setBusy(true);
    try {
      // Login authenticates on EMAIL (register takes username + email).
      const data = await api.login(demoEmail || email, demoEmail ? DEMO_PASSWORD : password);
      setToken(data.token);
      onAuth(data.token);
    } catch (e2) {
      setErr(
        e2.status === 401
          ? 'Wrong email or password.'
          : e2.message || 'Could not reach the API. Is the server running on :8080?'
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login-wrap">
      <form className="login" onSubmit={signIn}>
        <h1>Crisis<span style={{ color: 'var(--accent-ink)' }}>Link</span></h1>
        <p className="tag">Calamity response and relief coordination</p>

        {err && <div className="err">{err}</div>}

        <div className="field">
          <label htmlFor="email">Email</label>
          <input
            id="email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="you@example.com"
            autoComplete="username"
          />
        </div>
        <div className="field">
          <label htmlFor="pw">Password</label>
          <input
            id="pw"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
          />
        </div>
        <button className="primary" style={{ width: '100%' }} disabled={busy}>
          {busy ? 'Signing in...' : 'Sign in'}
        </button>

        <div className="demo-accounts">
          <div className="hint">Or explore a role</div>
          <div className="demo-grid">
            {DEMOS.map((d) => (
              <button key={d.email} type="button" disabled={busy} onClick={(e) => signIn(e, d.email)}>
                {d.label}
              </button>
            ))}
          </div>
        </div>
      </form>
    </div>
  );
}
