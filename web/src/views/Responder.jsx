import { useEffect, useState, useRef, useCallback } from 'react';
import { api, fmtMeters, fmtAgo } from '../api.js';
import MapView from '../components/MapView.jsx';
import { TopBar, useToast } from '../App.jsx';

// The heartbeat interval the backend is tuned for: TTL is 3x this, so a unit must
// miss TWO consecutive pings before it is declared dark.
const HEARTBEAT_MS = 10000;
const TTL_SECONDS = 30;

// Cap on how many dispatched incidents are searched for this responder's
// assignment. Bounded work beats an unbounded scan that gets slower exactly when
// the system is busiest.
const MAX_SCAN = 12;

const NEXT_STATUS = { reserved: 'en_route', en_route: 'on_scene', on_scene: 'completed' };
const STATUS_LABEL = { en_route: 'Mark en route', on_scene: 'Mark on scene', completed: 'Mark complete' };

export default function Responder({ claims, onLogout }) {
  const toast = useToast();
  const [unit, setUnit] = useState(null);
  const [assignment, setAssignment] = useState(null);
  const [incident, setIncident] = useState(null);
  const [broadcasting, setBroadcasting] = useState(false);
  const [presence, setPresence] = useState(null);
  const [pos, setPos] = useState(null);
  const posRef = useRef(null);
  posRef.current = pos;

  const unitId = claims.unitId;

  const loadUnit = useCallback(async () => {
    if (!unitId) return;
    try {
      const all = await api.listUnits();
      const u = all.find((x) => x.id === unitId);
      setUnit(u || null);
      if (u && !posRef.current) setPos([u.latitude, u.longitude]);
    } catch (e) {
      toast(e.message, 'err');
    }
  }, [unitId, toast]);

  // Find this responder's active dispatch.
  //
  // HONEST LIMITATION: there is no "my current assignment" endpoint, so this has
  // to search. The naive version issued one request PER dispatched incident on a
  // 5s timer — an N+1 that grew with the size of the disaster, which is exactly
  // backwards. Two things bound it now: the requests run concurrently instead of
  // serially, and only the most recent MAX_SCAN dispatched incidents are checked.
  // A responder holds at most one active dispatch, so the first hit wins.
  //
  // The real fix is a GET /me/assignment endpoint; this keeps the API unchanged.
  const loadAssignment = useCallback(async () => {
    if (!unitId) return;
    try {
      const incidents = await api.listIncidents(60);
      const dispatched = incidents.filter((i) => i.status === 'dispatched').slice(0, MAX_SCAN);

      const results = await Promise.all(
        dispatched.map((inc) =>
          api
            .incidentDispatches(inc.id)
            .then((ds) => ({ inc, ds: ds || [] }))
            .catch(() => ({ inc, ds: [] }))
        )
      );

      for (const { inc, ds } of results) {
        const mine = ds.find(
          (d) => d.unitId === unitId && ['reserved', 'en_route', 'on_scene'].includes(d.status)
        );
        if (mine) {
          setAssignment(mine);
          setIncident(inc);
          return;
        }
      }
      setAssignment(null);
      setIncident(null);
    } catch (e) {
      toast(e.message, 'err');
    }
  }, [unitId, toast]);

  const loadPresence = useCallback(async () => {
    if (!unitId) return;
    try {
      setPresence(await api.presence(unitId));
    } catch (e) {
      // 404 means the presence key EXPIRED — the unit has gone dark. Absence is
      // the event; there is nothing to poll for and no cleanup job involved.
      if (e.status === 404) setPresence(null);
    }
  }, [unitId]);

  useEffect(() => {
    loadUnit();
    loadAssignment();
    loadPresence();
    const t = setInterval(() => {
      loadAssignment();
      loadPresence();
    }, 5000);
    return () => clearInterval(t);
  }, [loadUnit, loadAssignment, loadPresence]);

  // Broadcasting: send a heartbeat every 10s, drifting slightly toward the
  // incident so the marker visibly MOVES on the operator's map.
  useEffect(() => {
    if (!broadcasting || !unitId) return;
    let cancelled = false;

    const beat = async () => {
      if (cancelled) return;
      let [lat, lng] = posRef.current || [28.6139, 77.209];
      if (incident) {
        lat += (incident.latitude - lat) * 0.25;
        lng += (incident.longitude - lng) * 0.25;
      }
      try {
        await api.heartbeat(unitId, lat, lng);
        setPos([lat, lng]);
        loadPresence();
      } catch (e) {
        toast(e.status === 403 ? 'You may only broadcast for your own unit (403)' : e.message, 'err');
        setBroadcasting(false);
      }
    };

    beat();
    const t = setInterval(beat, HEARTBEAT_MS);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, [broadcasting, unitId, incident, loadPresence, toast]);

  async function advance() {
    const next = NEXT_STATUS[assignment.status];
    try {
      await api.advanceDispatch(assignment.id, next);
      toast(`Status → ${next.replace('_', ' ')}`, 'ok');
      loadAssignment();
      loadUnit();
    } catch (e) {
      toast(e.message, 'err');
    }
  }

  if (!unitId) {
    return (
      <div className="app">
        <TopBar claims={claims} onLogout={onLogout} />
        <div className="center-scroll">
          <div className="inner">
            <div className="card">
              <h2>No unit assigned</h2>
              <p className="sub">
                Your account is a responder but isn't bound to a unit yet. An admin
                assigns the binding, and it travels inside your token — which is why
                you can act for that unit and no other.
              </p>
            </div>
          </div>
        </div>
      </div>
    );
  }

  const age = presence?.ageSeconds;
  const remaining = age != null ? Math.max(0, TTL_SECONDS - age) : 0;

  return (
    <div className="app">
      <TopBar
        claims={claims}
        onLogout={onLogout}
        extra={
          <span className="stat">
            <i className={`dot ${presence ? (remaining > 10 ? 'ok' : 'warn') : 'bad'}`} />
            {unit?.callSign || 'unit'}{' '}
            {presence ? <>· LIVE, seen {Math.round(age)}s ago</> : <>· DARK</>}
          </span>
        }
      />

      <div className="body">
        <div className="rail">
          <div className="rail-head">Position broadcast</div>
          <div style={{ padding: 14 }}>
            <button
              className={broadcasting ? 'danger' : 'primary'}
              style={{ width: '100%' }}
              onClick={() => setBroadcasting((b) => !b)}
            >
              {broadcasting ? '■ Stop broadcasting' : '⦿ Start broadcasting'}
            </button>
            <p className="dim" style={{ fontSize: 11.5, lineHeight: 1.5, marginTop: 12 }}>
              Sends a heartbeat every {HEARTBEAT_MS / 1000}s. The key lives {TTL_SECONDS}s —
              three intervals — so two missed pings are tolerated before you're
              treated as dark.
            </p>

            {presence ? (
              <>
                <div style={{ marginTop: 14, marginBottom: 6 }} className="dim">
                  presence key expires in <b className="mono">{Math.ceil(remaining)}s</b>
                </div>
                <div className="meter">
                  <div
                    className={remaining < 10 ? 'tight' : ''}
                    style={{ width: `${(remaining / TTL_SECONDS) * 100}%` }}
                  />
                </div>
              </>
            ) : (
              <div style={{ marginTop: 14 }} className="dim">
                No live key — you are invisible to dispatch. Stop broadcasting and
                watch this happen {TTL_SECONDS}s later.
              </div>
            )}
          </div>

          <div className="rail-head">My unit</div>
          <div style={{ padding: 14 }} className="dim">
            <div style={{ marginBottom: 6 }}>
              <b className="mono" style={{ color: 'var(--ink)' }}>{unit?.callSign || '—'}</b>
            </div>
            <div>type · {unit?.type || '—'}</div>
            <div>status · {unit?.status || '—'}</div>
          </div>
        </div>

        <div className="map-wrap">
          <MapView
            incidents={incident ? [incident] : []}
            units={unit ? [{ ...unit, latitude: pos?.[0] ?? unit.latitude, longitude: pos?.[1] ?? unit.longitude, isLive: !!presence }] : []}
            flyTo={pos}
          />
        </div>

        <div className="rail right">
          <div className="rail-head">Current assignment</div>
          {!assignment && <div className="empty">No active assignment.<br />Standing by.</div>}
          {assignment && incident && (
            <div style={{ padding: 14 }}>
              <span className={`sev ${incident.severity}`}>{incident.severity}</span>
              <h3 style={{ margin: '10px 0 6px', fontSize: 16 }}>{incident.title}</h3>
              <p className="dim" style={{ fontSize: 12.5, margin: '0 0 14px' }}>
                {incident.description}
              </p>

              <div className="cand-detail" style={{ marginBottom: 16 }}>
                <span><span className="k">dispatch</span> {assignment.status}</span>
                <span><span className="k">since</span> {fmtAgo(assignment.createdAt)}</span>
              </div>

              {NEXT_STATUS[assignment.status] ? (
                <button className="primary" style={{ width: '100%' }} onClick={advance}>
                  {STATUS_LABEL[NEXT_STATUS[assignment.status]]}
                </button>
              ) : (
                <div className="dim">Assignment closed.</div>
              )}

              <p className="dim" style={{ fontSize: 11, lineHeight: 1.5, marginTop: 14 }}>
                Completing frees your unit and resolves the incident in the same
                transaction — the cascade is guarded in SQL, so an already-cancelled
                incident is never resurrected.
              </p>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
