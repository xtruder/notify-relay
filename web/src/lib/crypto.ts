import { hkdf } from '@noble/hashes/hkdf'
import { sha256 } from '@noble/hashes/sha256'
import { XChaCha20Poly1305 } from '@stablelib/xchacha20poly1305'

const SALT = new TextEncoder().encode('notify-relay-v1')
const INFO = new TextEncoder().encode('message-encryption')
const KEY_SIZE = 32
const NONCE_SIZE = 24

export function deriveKey(channelID: string): Uint8Array {
  const ikm = new TextEncoder().encode(channelID)
  return hkdf(sha256, ikm, SALT, INFO, KEY_SIZE)
}

export function channelHash(channelID: string): string {
  const data = new TextEncoder().encode(channelID)
  return Array.from(sha256(data))
    .map(b => b.toString(16).padStart(2, '0'))
    .join('')
}

export function encrypt(key: Uint8Array, plaintext: Uint8Array): Uint8Array {
  const cipher = new XChaCha20Poly1305(key)
  const nonce = crypto.getRandomValues(new Uint8Array(NONCE_SIZE))
  const ciphertext = cipher.seal(nonce, plaintext)
  const result = new Uint8Array(nonce.length + ciphertext.length)
  result.set(nonce)
  result.set(ciphertext, nonce.length)
  return result
}

export function decrypt(key: Uint8Array, ciphertext: Uint8Array): Uint8Array {
  if (ciphertext.length < NONCE_SIZE) {
    throw new Error('ciphertext too short')
  }
  const cipher = new XChaCha20Poly1305(key)
  const nonce = ciphertext.slice(0, NONCE_SIZE)
  const ct = ciphertext.slice(NONCE_SIZE)
  const plaintext = cipher.open(nonce, ct)
  if (plaintext === null) {
    throw new Error('decryption failed')
  }
  return plaintext
}
