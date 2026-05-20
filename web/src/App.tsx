import { useState, useEffect, useRef, useCallback } from 'react'
import { RelayNode, type PeerInfo, type NotificationInput } from './lib/p2p'
import type { NotifyMessage } from './gen/notify_relay/v1/relay_pb.js'

function App() {
  const [channelKey, setChannelKey] = useState('')
  const [bootstrapPeer, setBootstrapPeer] = useState('')
  const [connected, setConnected] = useState(false)
  const [connecting, setConnecting] = useState(false)
  const [peerId, setPeerId] = useState<string | null>(null)
  const [connectionCount, setConnectionCount] = useState(0)
  const [peers, setPeers] = useState<PeerInfo[]>([])
  const [notifications, setNotifications] = useState<NotifyMessage[]>([])
  const [summary, setSummary] = useState('')
  const [body, setBody] = useState('')
  const nodeRef = useRef<RelayNode | null>(null)

  const connect = useCallback(async () => {
    if (!channelKey.trim()) return
    setConnecting(true)
    try {
      const node = new RelayNode()
      nodeRef.current = node

      const bootstraps = bootstrapPeer.trim()
        ? [bootstrapPeer.trim()]
        : undefined

      await node.start(channelKey.trim(), bootstraps)
      setPeerId(node.getPeerId())
      setConnected(true)

      node.onNotification((msg) => {
        setNotifications(prev => [msg, ...prev].slice(0, 100))
      })

      node.onPresence(() => {
        setPeers(node.getPeers())
      })
    } catch (err) {
      console.error('connection failed', err)
      alert('Connection failed: ' + (err instanceof Error ? err.message : String(err)))
    } finally {
      setConnecting(false)
    }
  }, [channelKey, bootstrapPeer])

  const disconnect = useCallback(async () => {
    await nodeRef.current?.stop()
    nodeRef.current = null
    setConnected(false)
    setPeerId(null)
    setConnectionCount(0)
    setPeers([])
  }, [])

  const send = useCallback(async () => {
    if (!summary.trim() || !nodeRef.current) return
    const notif: NotificationInput = {
      appName: 'notify-relay-web',
      replacesId: 0,
      appIcon: '',
      summary: summary.trim(),
      body: body.trim(),
      actions: [],
      hints: [],
      expireTimeout: -1,
      wait: false,
      printId: false,
    }
    try {
      await nodeRef.current.sendNotification(notif)
      setSummary('')
      setBody('')
    } catch (err) {
      alert('Send failed: ' + (err instanceof Error ? err.message : String(err)))
    }
  }, [summary, body])

  useEffect(() => {
    if (!connected) return
    const interval = setInterval(() => {
      setConnectionCount(nodeRef.current?.getConnections() ?? 0)
      setPeers(nodeRef.current?.getPeers() ?? [])
    }, 3000)
    return () => clearInterval(interval)
  }, [connected])

  if (!connected) {
    return (
      <div style={{ maxWidth: 480, margin: '60px auto', padding: 24, fontFamily: 'system-ui, sans-serif' }}>
        <h1>notify-relay</h1>
        <p>Connect to your notification mesh by entering the channel key.</p>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <label>
            Channel key
            <input
              type="text"
              value={channelKey}
              onChange={e => setChannelKey(e.target.value)}
              placeholder="acid-bold-crew-dome-fork"
              style={{ width: '100%', padding: 8, marginTop: 4, fontSize: 16 }}
            />
          </label>
          <label>
            Bootstrap peer (optional)
            <input
              type="text"
              value={bootstrapPeer}
              onChange={e => setBootstrapPeer(e.target.value)}
              placeholder="/dns4/your-vps.com/tcp/443/wss/p2p/12D3..."
              style={{ width: '100%', padding: 8, marginTop: 4, fontSize: 14 }}
            />
          </label>
          <button
            onClick={connect}
            disabled={connecting || !channelKey.trim()}
            style={{ padding: '10px 16px', fontSize: 16, cursor: 'pointer' }}
          >
            {connecting ? 'Connecting...' : 'Connect'}
          </button>
        </div>
      </div>
    )
  }

  return (
    <div style={{ maxWidth: 800, margin: '0 auto', padding: 24, fontFamily: 'system-ui, sans-serif' }}>
      <header style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 24 }}>
        <h1>notify-relay</h1>
        <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
          <span style={{ color: '#16a34a', fontWeight: 600 }}>Connected</span>
          <span style={{ color: '#6b7280', fontSize: 12 }}>{connectionCount} connections</span>
          <span style={{ color: '#6b7280', fontSize: 12 }} title={peerId ?? ''}>
            {peerId?.slice(0, 12)}...
          </span>
          <button onClick={disconnect} style={{ padding: '6px 12px' }}>Disconnect</button>
        </div>
      </header>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 24 }}>
        <section>
          <h2>Send notification</h2>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            <input
              type="text"
              value={summary}
              onChange={e => setSummary(e.target.value)}
              placeholder="Summary"
              style={{ padding: 8, fontSize: 14 }}
            />
            <textarea
              value={body}
              onChange={e => setBody(e.target.value)}
              placeholder="Body (optional)"
              rows={3}
              style={{ padding: 8, fontSize: 14 }}
            />
            <button onClick={send} disabled={!summary.trim()} style={{ padding: '8px 12px' }}>
              Send
            </button>
          </div>
        </section>

        <section>
          <h2>Peers ({peers.length})</h2>
          {peers.length === 0 ? (
            <p style={{ color: '#6b7280' }}>No peers discovered yet.</p>
          ) : (
            <ul style={{ listStyle: 'none', padding: 0, margin: 0 }}>
              {peers.map(p => (
                <li key={p.peerId} style={{ padding: '8px 0', borderBottom: '1px solid #e5e7eb' }}>
                  <div style={{ fontWeight: 600 }}>{p.hostname}</div>
                  <div style={{ fontSize: 12, color: '#6b7280' }}>
                    priority={p.priority} {p.isPresent ? 'present' : 'away'}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>

      <section style={{ marginTop: 24 }}>
        <h2>Notifications</h2>
        {notifications.length === 0 ? (
          <p style={{ color: '#6b7280' }}>No notifications received yet.</p>
        ) : (
          <ul style={{ listStyle: 'none', padding: 0, margin: 0 }}>
            {notifications.map((n, i) => (
              <li key={i} style={{ padding: '12px', marginBottom: 8, background: '#f9fafb', borderRadius: 6 }}>
                <div style={{ fontWeight: 600 }}>{n.notification?.summary}</div>
                {n.notification?.body && (
                  <div style={{ fontSize: 14, color: '#374151', marginTop: 4 }}>{n.notification.body}</div>
                )}
                <div style={{ fontSize: 11, color: '#9ca3af', marginTop: 6 }}>
                  from {n.senderPeerId?.slice(0, 12)}...
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}

export default App
