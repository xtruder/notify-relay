package p2p

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// PeerInfo holds known state about a remote peer.
type PeerInfo struct {
	PeerID    peer.ID
	Hostname  string
	IsPresent bool // whether the node can currently handle notifications (has dbus + unlocked)
	Priority  int
	LastSeen  time.Time
}

// PeerTable tracks presence information for all known peers
// and the local node's own info for condition evaluation.
type PeerTable struct {
	peers map[peer.ID]*PeerInfo
	local *PeerInfo // local node info (updated by presence service)
	mu    sync.RWMutex
}

// NewPeerTable creates a new peer table.
func NewPeerTable() *PeerTable {
	return &PeerTable{
		peers: make(map[peer.ID]*PeerInfo),
	}
}

// SetLocal sets the local node's info for condition evaluation.
func (pt *PeerTable) SetLocal(info PeerInfo) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	info.LastSeen = time.Now()
	pt.local = &info
}

// GetLocal returns the local node's info.
func (pt *PeerTable) GetLocal() (PeerInfo, bool) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	if pt.local == nil {
		return PeerInfo{}, false
	}
	return *pt.local, true
}

// Update inserts or updates a peer's presence info.
func (pt *PeerTable) Update(info PeerInfo) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	info.LastSeen = time.Now()
	pt.peers[info.PeerID] = &info
}

// Remove removes a peer from the table.
func (pt *PeerTable) Remove(id peer.ID) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	delete(pt.peers, id)
}

// Get returns info for a specific peer.
func (pt *PeerTable) Get(id peer.ID) (PeerInfo, bool) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	p, ok := pt.peers[id]
	if !ok {
		return PeerInfo{}, false
	}
	return *p, true
}

// GetAll returns a snapshot of all peers.
func (pt *PeerTable) GetAll() []PeerInfo {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	result := make([]PeerInfo, 0, len(pt.peers))
	for _, p := range pt.peers {
		result = append(result, *p)
	}
	return result
}

// HasPresentPeer returns true if any remote peer can display and is not stale.
func (pt *PeerTable) HasPresentPeer(staleTimeout time.Duration) bool {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	now := time.Now()
	for _, p := range pt.peers {
		if p.IsPresent && now.Sub(p.LastSeen) < staleTimeout {
			return true
		}
	}
	return false
}

// HasConnectedPeer returns true if any peer has been seen within staleTimeout.
func (pt *PeerTable) HasConnectedPeer(staleTimeout time.Duration) bool {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	now := time.Now()
	for _, p := range pt.peers {
		if now.Sub(p.LastSeen) < staleTimeout {
			return true
		}
	}
	return false
}

// BestPresentPeer returns the highest priority (lowest number) displayable, non-stale peer.
func (pt *PeerTable) BestPresentPeer(staleTimeout time.Duration) (PeerInfo, bool) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	now := time.Now()
	var best *PeerInfo
	for _, p := range pt.peers {
		if !p.IsPresent || now.Sub(p.LastSeen) >= staleTimeout {
			continue
		}
		if best == nil || p.Priority < best.Priority {
			best = p
		}
	}
	if best == nil {
		return PeerInfo{}, false
	}
	return *best, true
}

// IsBestActive returns true if the local node is the best (lowest priority number)
// unlocked node that can display among all known nodes (local + remote peers).
// Returns false if the local node is locked, cannot display, or if a remote
// unlocked+displayable peer has a lower priority number.
// Peers that cannot display (no dbus/desktop) are not competitors.
func (pt *PeerTable) IsBestActive(staleTimeout time.Duration) bool {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	if pt.local == nil || !pt.local.IsPresent {
		return false
	}

	now := time.Now()
	for _, p := range pt.peers {
		if !p.IsPresent || now.Sub(p.LastSeen) >= staleTimeout {
			continue
		}
		// A remote displayable peer with lower or equal priority number exists.
		// Use peer ID as tiebreaker for equal priority.
		if p.Priority < pt.local.Priority {
			return false
		}
		if p.Priority == pt.local.Priority && p.PeerID < pt.local.PeerID {
			return false
		}
	}

	return true
}

// IsNoBetterPeer returns true if:
// 1. No unlocked+displayable node exists anywhere (local or remote), AND
// 2. This node has the highest priority NUMBER (worst priority) among all online nodes.
// This is used for "last resort" nodes like a VPS that forwards to ntfy.sh.
func (pt *PeerTable) IsNoBetterPeer(staleTimeout time.Duration) bool {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	if pt.local == nil {
		return false
	}

	now := time.Now()

	// Check if any node (including local) can display
	if pt.local.IsPresent {
		return false
	}
	for _, p := range pt.peers {
		if now.Sub(p.LastSeen) >= staleTimeout {
			continue
		}
		if p.IsPresent {
			return false
		}
	}

	// No unlocked+displayable node exists. Check if we have the highest priority number
	// (worst priority = last resort).
	for _, p := range pt.peers {
		if now.Sub(p.LastSeen) >= staleTimeout {
			continue
		}
		if p.Priority > pt.local.Priority {
			return false
		}
		if p.Priority == pt.local.Priority && p.PeerID > pt.local.PeerID {
			return false
		}
	}

	return true
}

// CleanupStale removes peers not seen within the given timeout.
func (pt *PeerTable) CleanupStale(timeout time.Duration) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	now := time.Now()
	for id, p := range pt.peers {
		if now.Sub(p.LastSeen) >= timeout {
			delete(pt.peers, id)
		}
	}
}
