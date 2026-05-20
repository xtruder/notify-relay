package p2p

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func testPeerID(t *testing.T) peer.ID {
	t.Helper()
	_, pub, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	id, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("peer ID from key: %v", err)
	}
	return id
}

func TestPeerTableBasic(t *testing.T) {
	pt := NewPeerTable()
	id := testPeerID(t)

	pt.Update(PeerInfo{
		PeerID:    id,
		Hostname:  "test",
		IsPresent: true,
		Priority:  1,
	})

	info, ok := pt.Get(id)
	if !ok {
		t.Fatal("expected peer to be found")
	}
	if info.Hostname != "test" {
		t.Errorf("expected hostname 'test', got %q", info.Hostname)
	}

	all := pt.GetAll()
	if len(all) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(all))
	}

	pt.Remove(id)
	_, ok = pt.Get(id)
	if ok {
		t.Fatal("expected peer to be removed")
	}
}

func TestPeerTableHasPresentPeer(t *testing.T) {
	pt := NewPeerTable()
	id1 := testPeerID(t)
	id2 := testPeerID(t)

	pt.Update(PeerInfo{PeerID: id1, Hostname: "locked", IsPresent: false, Priority: 1})
	pt.Update(PeerInfo{PeerID: id2, Hostname: "unlocked", IsPresent: true, Priority: 2})

	if !pt.HasPresentPeer(30 * time.Second) {
		t.Fatal("expected unlocked peer")
	}

	pt.Update(PeerInfo{PeerID: id2, Hostname: "unlocked", IsPresent: false, Priority: 2})
	if pt.HasPresentPeer(30 * time.Second) {
		t.Fatal("expected no unlocked peers")
	}
}

func TestPeerTableBestUnlocked(t *testing.T) {
	pt := NewPeerTable()
	id1 := testPeerID(t)
	id2 := testPeerID(t)

	pt.Update(PeerInfo{PeerID: id1, Hostname: "high-priority", IsPresent: true, Priority: 1})
	pt.Update(PeerInfo{PeerID: id2, Hostname: "low-priority", IsPresent: true, Priority: 5})

	best, ok := pt.BestPresentPeer(30 * time.Second)
	if !ok {
		t.Fatal("expected to find best peer")
	}
	if best.PeerID != id1 {
		t.Error("expected high-priority peer to be best")
	}
	if best.Priority != 1 {
		t.Errorf("expected priority 1, got %d", best.Priority)
	}
}

func TestPeerTableStaleTimeout(t *testing.T) {
	pt := NewPeerTable()
	id := testPeerID(t)

	pt.Update(PeerInfo{PeerID: id, Hostname: "test", IsPresent: true, Priority: 1})

	// With a very short timeout, peer should be considered stale
	if pt.HasPresentPeer(0) {
		t.Fatal("peer should be stale with 0 timeout")
	}

	// With a long timeout, peer should be present
	if !pt.HasPresentPeer(10 * time.Second) {
		t.Fatal("peer should not be stale with 10s timeout")
	}
}

func TestPeerTableCleanupStale(t *testing.T) {
	pt := NewPeerTable()
	id := testPeerID(t)

	pt.Update(PeerInfo{PeerID: id, Hostname: "test", IsPresent: true, Priority: 1})

	// Force stale by using 0 timeout
	pt.CleanupStale(0)

	if pt.HasConnectedPeer(30 * time.Second) {
		t.Fatal("expected no peers after cleanup")
	}
}

func TestPeerTableLocalInfo(t *testing.T) {
	pt := NewPeerTable()
	localID := testPeerID(t)

	_, ok := pt.GetLocal()
	if ok {
		t.Fatal("expected no local info initially")
	}

	pt.SetLocal(PeerInfo{PeerID: localID, Hostname: "local", IsPresent: true, Priority: 1})

	local, ok := pt.GetLocal()
	if !ok {
		t.Fatal("expected local info")
	}
	if local.Hostname != "local" || local.Priority != 1 || !local.IsPresent {
		t.Errorf("unexpected local info: %+v", local)
	}
}

// TestIsBestActive tests the IsBestActive condition.
func TestIsBestActive(t *testing.T) {
	stale := 30 * time.Second

	t.Run("local unlocked, no peers", func(t *testing.T) {
		pt := NewPeerTable()
		localID := testPeerID(t)
		pt.SetLocal(PeerInfo{PeerID: localID, Hostname: "local", IsPresent: true, Priority: 1})

		if !pt.IsBestActive(stale) {
			t.Error("expected IsBestActive=true when local is unlocked with no peers")
		}
	})

	t.Run("local locked", func(t *testing.T) {
		pt := NewPeerTable()
		localID := testPeerID(t)
		pt.SetLocal(PeerInfo{PeerID: localID, Hostname: "local", IsPresent: false, Priority: 1})

		if pt.IsBestActive(stale) {
			t.Error("expected IsBestActive=false when local is locked")
		}
	})

	t.Run("local unlocked, remote has lower priority number", func(t *testing.T) {
		pt := NewPeerTable()
		localID := testPeerID(t)
		remoteID := testPeerID(t)
		pt.SetLocal(PeerInfo{PeerID: localID, Hostname: "local", IsPresent: true, Priority: 5})
		pt.Update(PeerInfo{PeerID: remoteID, Hostname: "remote", IsPresent: true, Priority: 1})

		if pt.IsBestActive(stale) {
			t.Error("expected IsBestActive=false when remote has lower priority number")
		}
	})

	t.Run("local unlocked with lowest priority number", func(t *testing.T) {
		pt := NewPeerTable()
		localID := testPeerID(t)
		remoteID := testPeerID(t)
		pt.SetLocal(PeerInfo{PeerID: localID, Hostname: "local", IsPresent: true, Priority: 1})
		pt.Update(PeerInfo{PeerID: remoteID, Hostname: "remote", IsPresent: true, Priority: 5})

		if !pt.IsBestActive(stale) {
			t.Error("expected IsBestActive=true when local has lowest priority number")
		}
	})

	t.Run("local unlocked, remote locked", func(t *testing.T) {
		pt := NewPeerTable()
		localID := testPeerID(t)
		remoteID := testPeerID(t)
		pt.SetLocal(PeerInfo{PeerID: localID, Hostname: "local", IsPresent: true, Priority: 5})
		pt.Update(PeerInfo{PeerID: remoteID, Hostname: "remote", IsPresent: false, Priority: 1})

		if !pt.IsBestActive(stale) {
			t.Error("expected IsBestActive=true when remote is locked (even with lower priority number)")
		}
	})
}

// TestIsNoBetterPeer tests the IsNoBetterPeer condition.
func TestIsNoBetterPeer(t *testing.T) {
	stale := 30 * time.Second

	t.Run("local unlocked — not last resort", func(t *testing.T) {
		pt := NewPeerTable()
		localID := testPeerID(t)
		pt.SetLocal(PeerInfo{PeerID: localID, Hostname: "vps", IsPresent: true, Priority: 50})

		if pt.IsNoBetterPeer(stale) {
			t.Error("expected IsNoBetterPeer=false when local is unlocked")
		}
	})

	t.Run("all locked, local has highest priority number", func(t *testing.T) {
		pt := NewPeerTable()
		localID := testPeerID(t)
		remoteID := testPeerID(t)
		pt.SetLocal(PeerInfo{PeerID: localID, Hostname: "vps", IsPresent: false, Priority: 50})
		pt.Update(PeerInfo{PeerID: remoteID, Hostname: "laptop", IsPresent: false, Priority: 1})

		if !pt.IsNoBetterPeer(stale) {
			t.Error("expected IsNoBetterPeer=true when all locked and local has highest priority number")
		}
	})

	t.Run("all locked, local does NOT have highest priority number", func(t *testing.T) {
		pt := NewPeerTable()
		localID := testPeerID(t)
		remoteID := testPeerID(t)
		pt.SetLocal(PeerInfo{PeerID: localID, Hostname: "laptop", IsPresent: false, Priority: 1})
		pt.Update(PeerInfo{PeerID: remoteID, Hostname: "vps", IsPresent: false, Priority: 50})

		if pt.IsNoBetterPeer(stale) {
			t.Error("expected IsNoBetterPeer=false when remote has higher priority number")
		}
	})

	t.Run("remote unlocked — not last resort", func(t *testing.T) {
		pt := NewPeerTable()
		localID := testPeerID(t)
		remoteID := testPeerID(t)
		pt.SetLocal(PeerInfo{PeerID: localID, Hostname: "vps", IsPresent: false, Priority: 50})
		pt.Update(PeerInfo{PeerID: remoteID, Hostname: "laptop", IsPresent: true, Priority: 1})

		if pt.IsNoBetterPeer(stale) {
			t.Error("expected IsNoBetterPeer=false when a remote peer is unlocked")
		}
	})

	t.Run("no local info set", func(t *testing.T) {
		pt := NewPeerTable()
		if pt.IsNoBetterPeer(stale) {
			t.Error("expected IsNoBetterPeer=false when no local info")
		}
	})

	t.Run("local locked, no remote peers", func(t *testing.T) {
		pt := NewPeerTable()
		localID := testPeerID(t)
		pt.SetLocal(PeerInfo{PeerID: localID, Hostname: "vps", IsPresent: false, Priority: 50})

		if !pt.IsNoBetterPeer(stale) {
			t.Error("expected IsNoBetterPeer=true when local is locked and no remote peers")
		}
	})
}
