package tui

import (
	"testing"

	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/project"
)

// PLAN 2.3: the forge sync follows the git update, never runs beside it.
func TestRefreshRunsGitUpdateThenForgeSync(t *testing.T) {
	m := model{sync: &forgesync.Service{}, svc: &project.Service{}, g: unicodeGlyphs}

	cmd := m.refresh()
	if cmd == nil || !m.updating {
		t.Fatalf("refresh must start the update: updating=%v cmd=%v", m.updating, cmd)
	}
	if m.syncing || !m.syncQueued {
		t.Fatalf("the sync must wait for the update: syncing=%v queued=%v", m.syncing, m.syncQueued)
	}

	// A second refresh while the update runs neither starts a second update
	// nor queues a second sync.
	if again := m.refresh(); again != nil {
		t.Error("a refresh during an update started something")
	}

	next, cmd := m.Update(updateDoneMsg{})
	m = next.(model)
	if m.updating || !m.syncing || m.syncQueued {
		t.Fatalf("update done must hand over to the sync: updating=%v syncing=%v queued=%v", m.updating, m.syncing, m.syncQueued)
	}
	if cmd == nil {
		t.Fatal("no command after the update")
	}
}

func TestRefreshWhileOnlyASyncRunsStartsTheUpdateAlongside(t *testing.T) {
	m := model{sync: &forgesync.Service{}, svc: &project.Service{}, g: unicodeGlyphs, syncing: true}
	if cmd := m.refresh(); cmd == nil || !m.updating {
		t.Fatalf("update not started: %v", m.updating)
	}
	if m.syncQueued {
		t.Error("a sync is already running: nothing to queue")
	}
}

func TestRefreshWithoutForgeNeverQueuesASync(t *testing.T) {
	m := model{svc: &project.Service{}, g: unicodeGlyphs}
	m.refresh()
	if m.syncQueued {
		t.Fatal("no forge service: nothing to sync")
	}
	next, _ := m.Update(updateDoneMsg{})
	if next.(model).syncing {
		t.Fatal("sync started without a service")
	}
}
