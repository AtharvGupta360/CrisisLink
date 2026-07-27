import { useEffect, useState } from 'react';
import { MapContainer, TileLayer, CircleMarker, useMapEvents } from 'react-leaflet';
import { api, fmtAgo, fmtMeters } from '../api.js';
import { TopBar, useToast } from '../App.jsx';

// Click-to-place pin. A citizen reporting an emergency should not have to type
// coordinates, so the map IS the location input.
function PinPicker({ pos, setPos }) {
  useMapEvents({ click: (e) => setPos([e.latlng.lat, e.latlng.lng]) });
  return pos ? (
    <CircleMarker
      center={pos}
      radius={11}
      pathOptions={{ color: '#e5534b', fillColor: '#e5534b', fillOpacity: 0.6, weight: 3 }}
    />
  ) : null;
}

export default function Citizen({ claims, onLogout }) {
  const toast = useToast();
  const [pos, setPos] = useState([28.6139, 77.209]);
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [severity, setSeverity] = useState('high');
  const [busy, setBusy] = useState(false);
  const [mine, setMine] = useState([]);
  const [shelters, setShelters] = useState([]);

  async function load() {
    try {
      const all = await api.listIncidents(60);
      // Only your own reports. The API returns the list; the ownership filter here
      // is a convenience — a citizen cannot act on anyone else's incident anyway,
      // because every mutating route is role-gated server-side.
      setMine(all.filter((i) => i.reporterId === claims.userId));
    } catch (e) {
      toast(e.message, 'err');
    }
    try {
      setShelters((await api.listShelters()).filter((s) => s.status === 'open').slice(0, 5));
    } catch {
      /* shelters are a nice-to-have on this screen */
    }
  }

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function submit(e) {
    e.preventDefault();
    if (!title.trim()) return toast('Describe what is happening', 'err');
    setBusy(true);
    try {
      const created = await api.reportIncident({
        title,
        description: description || title,
        severity,
        latitude: pos[0],
        longitude: pos[1],
      });
      // The API tells you when your report was MERGED into an existing incident
      // rather than creating a new one — geo-deduplication, surfaced to the user.
      if (created.reportCount > 1) {
        toast(`Merged — ${created.reportCount} people have reported this`, 'ok');
      } else {
        toast('Report sent. Help is being coordinated.', 'ok');
      }
      setTitle('');
      setDescription('');
      load();
    } catch (e2) {
      toast(e2.message, 'err');
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="app">
      <TopBar claims={claims} onLogout={onLogout} />
      <div className="body">
        <div className="center-scroll">
          <div className="inner">
            <div className="card">
              <h2>Report an emergency</h2>
              <p className="sub">Tap the map to place your location, then describe what you see.</p>

              <div style={{ height: 260, borderRadius: 6, overflow: 'hidden', marginBottom: 16 }}>
                <MapContainer center={pos} zoom={13} className="leaflet-container" preferCanvas>
                  <TileLayer
                    url="https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png"
                    attribution="&copy; OpenStreetMap &copy; CARTO"
                  />
                  <PinPicker pos={pos} setPos={setPos} />
                </MapContainer>
              </div>
              <div className="dim mono" style={{ fontSize: 11, marginBottom: 14 }}>
                📍 {pos[0].toFixed(4)}, {pos[1].toFixed(4)}
              </div>

              <form onSubmit={submit}>
                <div className="field">
                  <label htmlFor="t">What is happening?</label>
                  <input id="t" value={title} onChange={(e) => setTitle(e.target.value)}
                    placeholder="Building collapse" />
                </div>
                <div className="field">
                  <label htmlFor="s">How serious is it?</label>
                  <select id="s" value={severity} onChange={(e) => setSeverity(e.target.value)}>
                    <option value="critical">Critical — lives at immediate risk</option>
                    <option value="high">High — urgent</option>
                    <option value="medium">Medium</option>
                    <option value="low">Low</option>
                  </select>
                </div>
                <div className="field">
                  <label htmlFor="d">Details</label>
                  <textarea id="d" rows={3} value={description}
                    onChange={(e) => setDescription(e.target.value)}
                    placeholder="Two floors down, people inside" />
                </div>
                <button className="primary" disabled={busy}>
                  {busy ? 'Sending…' : 'Send report'}
                </button>
              </form>
            </div>

            <div className="card">
              <h2>My reports</h2>
              <p className="sub">Status updates as responders are assigned.</p>
              {mine.length === 0 && <div className="empty">You haven't reported anything yet.</div>}
              {mine.map((i) => (
                <div key={i.id} className="row" style={{ cursor: 'default' }}>
                  <div className="row-title">{i.title}</div>
                  <div className="row-meta">
                    <span className={`sev ${i.severity}`}>{i.severity}</span>
                    <span className="pill">{i.status}</span>
                    {i.reportCount > 1 && (
                      <span className="pill count">{i.reportCount} people reported this</span>
                    )}
                    <span className="time">{fmtAgo(i.createdAt)}</span>
                  </div>
                </div>
              ))}
            </div>

            <div className="card">
              <h2>Open shelters</h2>
              <p className="sub">Places with space right now.</p>
              {shelters.length === 0 && <div className="empty">No open shelters listed.</div>}
              {shelters.map((s) => (
                <div key={s.id} className="row" style={{ cursor: 'default' }}>
                  <div className="row-title">{s.name}</div>
                  <div className="row-meta">
                    <span className="pill">
                      {s.capacity - s.occupancy} of {s.capacity} beds free
                    </span>
                    {s.distanceMeters != null && (
                      <span className="time">{fmtMeters(s.distanceMeters)}</span>
                    )}
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
