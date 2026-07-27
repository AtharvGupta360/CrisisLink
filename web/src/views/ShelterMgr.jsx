import { useEffect, useState, useCallback } from 'react';
import { api, fmtAgo, POLL_MS } from '../api.js';
import { TopBar, useToast } from '../App.jsx';

export default function ShelterMgr({ claims, onLogout }) {
  const toast = useToast();
  const [shelter, setShelter] = useState(null);
  const [victims, setVictims] = useState([]);
  const [busyId, setBusyId] = useState(null);

  const load = useCallback(async () => {
    try {
      const all = await api.listShelters();
      setShelter(all.find((s) => s.id === claims.shelterId) || null);
    } catch (e) {
      toast(e.message, 'err');
    }
    try {
      // The API scopes this list to the caller's shelter IN SQL — a manager never
      // receives rows for other shelters, so there is no client-side filter to
      // forget. PII stays out of the process entirely.
      const v = await api.listVictims();
      setVictims((v || []).filter((x) => !x.shelterId));
    } catch (e) {
      if (e.status !== 403) toast(e.message, 'err');
    }
  }, [claims.shelterId, toast]);

  useEffect(() => {
    load();
    const t = setInterval(load, POLL_MS);
    return () => clearInterval(t);
  }, [load]);

  async function admit(v) {
    setBusyId(v.id);
    try {
      await api.assignVictim(v.id, claims.shelterId);
      toast(`${v.name} admitted`, 'ok');
      load();
    } catch (e) {
      // 409 here is the capacity guard doing its job: occupancy < capacity is
      // checked and incremented in ONE atomic statement, so a full shelter simply
      // affects zero rows rather than over-admitting.
      toast(e.status === 409 ? `Shelter is full — ${v.name} not admitted` : e.message, 'err');
      load();
    } finally {
      setBusyId(null);
    }
  }

  async function toggleStatus() {
    const next = shelter.status === 'open' ? 'closed' : 'open';
    try {
      await api.setShelterStatus(shelter.id, next);
      toast(`Shelter ${next}`, 'ok');
      load();
    } catch (e) {
      toast(e.message, 'err');
    }
  }

  if (!claims.shelterId) {
    return (
      <div className="app">
        <TopBar claims={claims} onLogout={onLogout} />
        <div className="center-scroll">
          <div className="inner">
            <div className="card">
              <h2>No shelter assigned</h2>
              <p className="sub">
                Your account isn't bound to a shelter yet. The binding lives in your
                token, which is what limits you to your own shelter's records.
              </p>
            </div>
          </div>
        </div>
      </div>
    );
  }

  const pct = shelter ? (shelter.occupancy / shelter.capacity) * 100 : 0;
  const free = shelter ? shelter.capacity - shelter.occupancy : 0;
  // `full` must be FALSE while the shelter is still loading. Deriving it from a
  // null shelter made free = 0, which rendered every Admit button as a disabled
  // "Full" on first paint — telling a manager the shelter was at capacity before
  // we knew anything about it.
  const full = shelter ? free <= 0 : false;
  // A closed shelter also refuses admissions server-side (the guard is
  // `status = 'open' AND occupancy < capacity`), so reflect that in the UI
  // instead of letting the click fail.
  const canAdmit = !!shelter && !full && shelter.status === 'open';

  return (
    <div className="app">
      <TopBar
        claims={claims}
        onLogout={onLogout}
        extra={
          shelter && (
            <span className="stat">
              <i className={`dot ${full ? 'bad' : free < 5 ? 'warn' : 'ok'}`} />
              <b>{shelter.occupancy}</b>/{shelter.capacity} occupied
            </span>
          )
        }
      />
      <div className="body">
        <div className="center-scroll">
          <div className="inner">
            {!shelter && <div className="card"><div className="empty">Loading shelter…</div></div>}

            {shelter && (
              <div className="card">
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 12 }}>
                  <div>
                    <h2>{shelter.name}</h2>
                    <p className="sub">
                      {full ? 'At capacity — admissions will be refused.' : `${free} beds available.`}
                    </p>
                  </div>
                  <button onClick={toggleStatus} className={shelter.status === 'open' ? '' : 'primary'}>
                    {shelter.status === 'open' ? 'Close shelter' : 'Reopen shelter'}
                  </button>
                </div>

                <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12 }} className="dim">
                  <span>occupancy</span>
                  <span className="mono">
                    {shelter.occupancy} / {shelter.capacity}
                  </span>
                </div>
                <div className="meter" style={{ marginTop: 6, height: 14 }}>
                  <div className={full ? 'full' : pct > 85 ? 'tight' : ''} style={{ width: `${pct}%` }} />
                </div>
                <div style={{ marginTop: 10 }}>
                  <span className="pill">{shelter.status}</span>
                </div>
              </div>
            )}

            <div className="card">
              <h2>Awaiting placement</h2>
              <p className="sub">
                People registered but not yet assigned to a shelter. You only ever
                receive records your role is scoped to.
              </p>
              {victims.length === 0 && <div className="empty">Nobody is awaiting placement.</div>}
              {victims.map((v) => (
                <div
                  key={v.id}
                  className="row"
                  style={{ cursor: 'default', display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 12 }}
                >
                  <div>
                    <div className="row-title">{v.name}</div>
                    <div className="row-meta">
                      <span className="pill">{v.status}</span>
                      <span className="time">{fmtAgo(v.createdAt)}</span>
                    </div>
                  </div>
                  <button
                    className="primary"
                    disabled={busyId === v.id || !canAdmit}
                    onClick={() => admit(v)}
                  >
                    {busyId === v.id
                      ? 'Admitting…'
                      : full
                        ? 'Full'
                        : shelter && shelter.status !== 'open'
                          ? 'Closed'
                          : 'Admit'}
                  </button>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
