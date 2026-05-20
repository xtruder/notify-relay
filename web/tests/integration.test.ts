import { describe, it, expect, beforeAll, afterAll } from 'vitest'
import { spawn, type ChildProcess } from 'child_process'
import { createLibp2p } from 'libp2p'
import { webSockets } from '@libp2p/websockets'
import { noise } from '@chainsafe/libp2p-noise'
import { yamux } from '@chainsafe/libp2p-yamux'
import { gossipsub } from '@chainsafe/libp2p-gossipsub'
import { identify } from '@libp2p/identify'
import type { GossipSub } from '@chainsafe/libp2p-gossipsub'
import { fromJsonString } from '@bufbuild/protobuf'
import { PresenceMessageSchema } from '../src/gen/notify_relay/v1/relay_pb.js'
import { deriveKey, channelHash, decrypt } from '../src/lib/crypto.js'
import { multiaddr } from '@multiformats/multiaddr'
import getPort from 'get-port'
import * as path from 'path'
import * as url from 'url'
import * as fs from 'fs'

const CHANNEL = 'test-integration-channel'
const TEST_TIMEOUT = 30000

const __dirname = path.dirname(url.fileURLToPath(import.meta.url))

interface GoRelay {
  proc: ChildProcess
  peerId: string
  multiaddr: string
}

async function buildGoBinary(): Promise<void> {
  return new Promise((resolve, reject) => {
    const repoRoot = path.resolve(__dirname, '../..')
    const proc = spawn('go', ['build', '-o', '/tmp/notify-relay-integration-test', './cmd/notify-relay'], {
      cwd: repoRoot,
    })
    proc.on('exit', (code) => {
      if (code === 0) resolve()
      else reject(new Error(`go build exited with ${code}`))
    })
  })
}

async function startGoRelay(channel: string, port: number): Promise<GoRelay> {
  return new Promise((resolve, reject) => {
    const homeDir = `/tmp/notify-relay-test-${Date.now()}`
    const configPath = `${homeDir}/.config/notify-relay.toml`
    const configContent = `[p2p]
channel = "${channel}"
priority = 50
listen = ["/ip4/127.0.0.1/tcp/${port}/ws"]
`
    fs.mkdirSync(path.dirname(configPath), { recursive: true })
    fs.writeFileSync(configPath, configContent)

    const proc = spawn('/tmp/notify-relay-integration-test', [
      'serve',
      '--config', configPath,
      '--relay',
    ], {
      env: { ...process.env, HOME: homeDir },
    })

    let stdout = ''
    const tryParse = () => {
      const match = stdout.match(/libp2p host started\s+peerID=([^\s]+)\s+addrs=\[(.*?)\]/)
      if (match) {
        const peerId = match[1]
        const addrs = match[2]
        const wsAddr = addrs.match(/\/ip4\/127\.0\.0\.1\/tcp\/\d+\/ws/)?.[0]
        if (wsAddr) {
          resolve({ proc, peerId, multiaddr: `${wsAddr}/p2p/${peerId}` })
        }
      }
    }

    proc.stdout.on('data', (data: Buffer) => {
      stdout += data.toString()
      tryParse()
    })

    proc.stderr.on('data', (data: Buffer) => {
      stdout += data.toString()
      tryParse()
    })

    setTimeout(() => reject(new Error('timeout starting Go relay. Output:\n' + stdout)), 10000)
  })
}

function stopGoRelay(relay: GoRelay): Promise<void> {
  return new Promise((resolve) => {
    relay.proc.kill('SIGTERM')
    relay.proc.on('exit', () => resolve())
    setTimeout(() => {
      relay.proc.kill('SIGKILL')
      resolve()
    }, 5000)
  })
}

describe('go relay + js-libp2p client', () => {
  let relay: GoRelay
  let jsNode: Awaited<ReturnType<typeof createLibp2p>>
  const key = deriveKey(CHANNEL)
  const hash = channelHash(CHANNEL)

  beforeAll(async () => {
    await buildGoBinary()
    const port = await getPort()
    relay = await startGoRelay(CHANNEL, port)

    jsNode = await createLibp2p({
      transports: [webSockets()],
      connectionEncrypters: [noise()],
      streamMuxers: [yamux()],
      services: {
        identify: identify(),
        pubsub: gossipsub({ allowPublishToZeroTopicPeers: true }),
      },
    })

    const ma = multiaddr(relay.multiaddr)
    await jsNode.dial(ma)
  }, TEST_TIMEOUT)

  afterAll(async () => {
    await jsNode?.stop()
    if (relay) await stopGoRelay(relay)
  }, TEST_TIMEOUT)

  it('should receive presence heartbeat from Go relay', async () => {
    const presenceTopicName = `notify-relay/${hash}/presence`
    const gs = jsNode.services.pubsub as GossipSub
    await gs.subscribe(presenceTopicName)

    const received = await new Promise<boolean>((resolve) => {
      const handler = (evt: any) => {
        try {
          const { topic, data } = evt.detail
          if (topic !== presenceTopicName) return
          const plaintext = decrypt(key, data)
          const msg = fromJsonString(PresenceMessageSchema, new TextDecoder().decode(plaintext))
          if (msg.peerId === relay.peerId) {
            gs.removeEventListener('message', handler)
            resolve(true)
          }
        } catch {
          // ignore
        }
      }
      gs.addEventListener('message', handler)
      setTimeout(() => {
        gs.removeEventListener('message', handler)
        resolve(false)
      }, 15000)
    })

    expect(received).toBe(true)
  }, TEST_TIMEOUT)

  it('should see Go relay in GossipSub topic peers', async () => {
    const presenceTopicName = `notify-relay/${hash}/presence`
    const gs = jsNode.services.pubsub as GossipSub

    // Give mesh a moment to form
    await new Promise(r => setTimeout(r, 2000))

    const peers = gs.getPeers(presenceTopicName)
    const hasRelay = peers.some((p: any) => p.toString() === relay.peerId)
    expect(hasRelay).toBe(true)
  }, TEST_TIMEOUT)
})
