import { createLibp2p, type Libp2pOptions } from 'libp2p'
import { webSockets } from '@libp2p/websockets'
import { webTransport } from '@libp2p/webtransport'
import { webRTC } from '@libp2p/webrtc'
import { circuitRelayTransport } from '@libp2p/circuit-relay-v2'
import { noise } from '@chainsafe/libp2p-noise'
import { yamux } from '@chainsafe/libp2p-yamux'
import { gossipsub } from '@chainsafe/libp2p-gossipsub'
import { kadDHT } from '@libp2p/kad-dht'
import { bootstrap } from '@libp2p/bootstrap'
import { identify } from '@libp2p/identify'
import type { Libp2p } from 'libp2p'
import type { GossipSub } from '@chainsafe/libp2p-gossipsub'
import type { KadDHT } from '@libp2p/kad-dht'
import { create, toJsonString, fromJsonString } from '@bufbuild/protobuf'
import { NotifyMessageSchema, PresenceMessageSchema, type NotifyMessage, type PresenceMessage } from '../gen/notify_relay/v1/relay_pb.js'
import { deriveKey, channelHash, encrypt, decrypt } from './crypto.js'

const PUBLIC_BOOTSTRAP_NODES = [
  '/dns4/sjc-2.bootstrap.libp2p.io/tcp/443/wss/p2p/QmZa1sAxajnQjVM8WjWXoMbmPd7NsWhfKsPkErzpm9wGkp',
  '/dns4/nyc-2.bootstrap.libp2p.io/tcp/443/wss/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN',
  '/dns4/ams-2.bootstrap.libp2p.io/tcp/443/wss/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa',
]

export type PeerInfo = {
  peerId: string
  hostname: string
  isPresent: boolean
  priority: number
  lastSeen: number
}

export type NotificationInput = {
  appName: string
  replacesId: number
  appIcon: string
  summary: string
  body: string
  actions: string[]
  hints: { name: string; type: string; value: string }[]
  expireTimeout: number
  wait: boolean
  printId: boolean
}

export class RelayNode {
  private node: Libp2p<{ pubsub: GossipSub; dht: KadDHT; identify: ReturnType<typeof identify> }> | null = null
  private key: Uint8Array | null = null
  private notificationTopic: any = null
  private presenceTopic: any = null
  private presenceInterval: ReturnType<typeof setInterval> | null = null
  private onNotificationCb: ((msg: NotifyMessage) => void) | null = null
  private onPresenceCb: ((msg: PresenceMessage) => void) | null = null
  private peers: Map<string, PeerInfo> = new Map()
  private hostname: string = `web-${Math.random().toString(36).slice(2, 8)}`

  async start(channelId: string, bootstrapPeers?: string[]): Promise<void> {
    this.key = deriveKey(channelId)

    const bootstraps = bootstrapPeers && bootstrapPeers.length > 0
      ? bootstrapPeers
      : PUBLIC_BOOTSTRAP_NODES

    const node = await createLibp2p({
      transports: [
        webSockets(),
        webTransport(),
        webRTC(),
        circuitRelayTransport(),
      ],
      connectionEncrypters: [noise()],
      streamMuxers: [yamux()],
      peerDiscovery: [
        bootstrap({
          list: bootstraps,
          timeout: 0,
        }),
      ],
      services: {
        identify: identify(),
        pubsub: gossipsub({
          allowPublishToZeroTopicPeers: true,
        }),
        dht: kadDHT({
          clientMode: true,
        }),
      },
    } as Libp2pOptions)

    this.node = node as Libp2p<{ pubsub: GossipSub; dht: KadDHT; identify: ReturnType<typeof identify> }>

    const hash = channelHash(channelId)
    const notificationsTopicName = `notify-relay/${hash}/notifications`
    const presenceTopicName = `notify-relay/${hash}/presence`

    this.notificationTopic = await this.node.services.pubsub.subscribe(notificationsTopicName)
    this.presenceTopic = await this.node.services.pubsub.subscribe(presenceTopicName)

    this.startNotificationHandler()
    this.startPresenceHandler()
    this.startPresenceHeartbeat()
  }

  private startNotificationHandler() {
    if (!this.notificationTopic) return
    this.notificationTopic.addEventListener('data', async (evt: any) => {
      try {
        const data = evt.detail.data as Uint8Array
        const plaintext = decrypt(this.key!, data)
        const msg = fromJsonString(NotifyMessageSchema, new TextDecoder().decode(plaintext))
        if (msg.senderPeerId === this.node!.peerId.toString()) return
        this.onNotificationCb?.(msg)
      } catch {
        // ignore decrypt/parse failures
      }
    })
  }

  private startPresenceHandler() {
    if (!this.presenceTopic) return
    this.presenceTopic.addEventListener('data', async (evt: any) => {
      try {
        const data = evt.detail.data as Uint8Array
        const plaintext = decrypt(this.key!, data)
        const msg = fromJsonString(PresenceMessageSchema, new TextDecoder().decode(plaintext))
        if (msg.peerId === this.node!.peerId.toString()) return

        if (msg.leaving) {
          this.peers.delete(msg.peerId)
        } else {
          this.peers.set(msg.peerId, {
            peerId: msg.peerId,
            hostname: msg.hostname,
            isPresent: msg.isPresent,
            priority: msg.priority,
            lastSeen: Date.now(),
          })
        }
        this.onPresenceCb?.(msg)
      } catch {
        // ignore decrypt/parse failures
      }
    })
  }

  private startPresenceHeartbeat() {
    this.publishPresence()
    this.presenceInterval = setInterval(() => {
      this.publishPresence()
    }, 10000)
  }

  private publishPresence() {
    if (!this.presenceTopic || !this.node || !this.key) return
    const msg = create(PresenceMessageSchema, {
      peerId: this.node.peerId.toString(),
      hostname: this.hostname,
      isPresent: false,
      priority: 100,
      timestamp: BigInt(Date.now()),
    })
    const data = new TextEncoder().encode(toJsonString(PresenceMessageSchema, msg))
    const encrypted = encrypt(this.key, data)
    this.presenceTopic.publish(encrypted).catch(() => {})
  }

  async sendNotification(notification: NotificationInput): Promise<void> {
    if (!this.notificationTopic || !this.node || !this.key) {
      throw new Error('node not started')
    }
    const msg = create(NotifyMessageSchema, {
      type: 'notification',
      msgId: `${this.node.peerId.toString()}-${Date.now()}`,
      senderPeerId: this.node.peerId.toString(),
      notification: {
        appName: notification.appName,
        replacesId: notification.replacesId,
        appIcon: notification.appIcon,
        summary: notification.summary,
        body: notification.body,
        actions: notification.actions,
        hints: notification.hints.map(h => ({ name: h.name, type: h.type, value: h.value })),
        expireTimeout: notification.expireTimeout,
        wait: notification.wait,
        printId: notification.printId,
      },
      actionKey: '',
      event: '',
      reason: 0,
    })
    const data = new TextEncoder().encode(toJsonString(NotifyMessageSchema, msg))
    const encrypted = encrypt(this.key, data)
    await this.notificationTopic.publish(encrypted)
  }

  onNotification(cb: (msg: NotifyMessage) => void) {
    this.onNotificationCb = cb
  }

  onPresence(cb: (msg: PresenceMessage) => void) {
    this.onPresenceCb = cb
  }

  getPeers(): PeerInfo[] {
    const now = Date.now()
    return Array.from(this.peers.values()).filter(p => now - p.lastSeen < 30000)
  }

  getPeerId(): string | null {
    return this.node?.peerId.toString() ?? null
  }

  getConnections(): number {
    return this.node?.getConnections().length ?? 0
  }

  async stop(): Promise<void> {
    if (this.presenceInterval) {
      clearInterval(this.presenceInterval)
    }
    if (this.presenceTopic && this.node && this.key) {
      const msg = create(PresenceMessageSchema, {
        peerId: this.node.peerId.toString(),
        hostname: this.hostname,
        isPresent: false,
        priority: 100,
        leaving: true,
        timestamp: BigInt(Date.now()),
      })
      const data = new TextEncoder().encode(toJsonString(PresenceMessageSchema, msg))
      const encrypted = encrypt(this.key, data)
      await this.presenceTopic.publish(encrypted).catch(() => {})
    }
    await this.node?.stop()
    this.node = null
  }
}
