package links

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/migrate"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// maxDev is the device cap the tests pass, like the app option default.
const maxDev = 10

func newDevice(by string) model.LinkDevice {
	return model.LinkDevice{Label: "Anna", Device: "iPhone, Safari", ApprovedBy: by}
}

func claimLink(t *testing.T, s *Store) model.Link {
	t.Helper()
	d := draft()
	d.Approval = model.Approval{SelfService: true}
	return mustCreate(t, s, d, "")
}

// The admin may set Approval on create and edit, but never Devices: a
// client could otherwise approve its own device.
func TestEditableApprovalNotDevices(t *testing.T) {
	s := newStore(t, t.TempDir())
	d := draft()
	d.Approval = model.Approval{Manual: true}
	d.Devices = []model.LinkDevice{{ID: "evil", ApprovedAt: t0, ApprovedBy: "admin"}}
	l := mustCreate(t, s, d, "")
	if !l.Approval.Manual || len(l.Devices) != 0 {
		t.Fatalf("create: approval %+v, devices %+v", l.Approval, l.Devices)
	}
	dev, err := s.AddDevice(l.ID, newDevice(model.ApprovedByAdmin), maxDev)
	if err != nil {
		t.Fatal(err)
	}
	edit := draft()
	edit.Approval = model.Approval{SelfService: true}
	edit.Devices = []model.LinkDevice{{ID: "evil", ApprovedAt: t0, ApprovedBy: "admin"}}
	got, err := s.Update(l.ID, edit)
	if err != nil {
		t.Fatal(err)
	}
	if got.Approval != (model.Approval{SelfService: true}) {
		t.Errorf("approval not edited: %+v", got.Approval)
	}
	if len(got.Devices) != 1 || got.Devices[0].ID != dev.ID {
		t.Errorf("edit changed devices: %+v", got.Devices)
	}
	edit.Devices = nil
	if got, _ := s.Update(l.ID, edit); len(got.Devices) != 1 {
		t.Errorf("edit with no devices dropped them: %+v", got.Devices)
	}
}

func TestAddDevice(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	l := mustCreate(t, s, draft(), "")
	d, err := s.AddDevice(l.ID, newDevice(model.ApprovedByHA), maxDev)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.ID) != 16 || !d.ApprovedAt.Equal(t0) || d.ApprovedBy != "ha" || d.Label != "Anna" {
		t.Errorf("added %+v", d)
	}
	if !s.HasDevice(l.ID, d.ID) || s.HasDevice(l.ID, "nope") || s.HasDevice("nope", d.ID) {
		t.Error("HasDevice wrong")
	}
	if !newStore(t, dir).HasDevice(l.ID, d.ID) {
		t.Error("device not saved")
	}
	for i := 1; i < maxDev; i++ {
		if _, err := s.AddDevice(l.ID, newDevice(model.ApprovedByAdmin), maxDev); err != nil {
			t.Fatalf("device %d: %v", i+1, err)
		}
	}
	if _, err := s.AddDevice(l.ID, newDevice(model.ApprovedByAdmin), maxDev); !errors.Is(err, ErrTooManyDevices) {
		t.Errorf("device %d: %v", maxDev+1, err)
	}
	if _, err := s.AddDevice("nope", newDevice(model.ApprovedByAdmin), maxDev); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown link: %v", err)
	}
	bad := newDevice("root")
	if _, err := s.AddDevice(l.ID, bad, maxDev); err == nil {
		t.Error("bad approver saved")
	}
}

func TestAddDeviceInactiveLink(t *testing.T) {
	s := newStore(t, t.TempDir())
	l := mustCreate(t, s, draft(), "")
	if err := s.Revoke(l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDevice(l.ID, newDevice(model.ApprovedByAdmin), maxDev); !errors.Is(err, ErrInactive) {
		t.Errorf("revoked link: %v", err)
	}
}

func TestClaimDevice(t *testing.T) {
	s := newStore(t, t.TempDir())
	manual := mustCreate(t, s, func() model.Link { d := draft(); d.Approval.Manual = true; return d }(), "")
	if _, err := s.ClaimDevice(manual.ID, newDevice(model.ApprovedByClaim), maxDev); !errors.Is(err, ErrClaimed) {
		t.Errorf("claim on a manual link: %v", err)
	}
	l := claimLink(t, s)
	if _, err := s.ClaimDevice(l.ID, newDevice(model.ApprovedByClaim), maxDev); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDevice(l.ID, newDevice(model.ApprovedByClaim), maxDev); !errors.Is(err, ErrClaimed) {
		t.Errorf("second claim: %v", err)
	}
	if err := s.ResetDevices(l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDevice(l.ID, newDevice(model.ApprovedByClaim), maxDev); err != nil {
		t.Errorf("claim after reset: %v", err)
	}
}

// Of N parallel claims, exactly one wins.
func TestClaimDeviceConcurrentExactlyOnce(t *testing.T) {
	s := newStore(t, t.TempDir())
	l := claimLink(t, s)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ClaimDevice(l.ID, newDevice(model.ApprovedByClaim), maxDev)
			switch {
			case err == nil:
				wins.Add(1)
			case !errors.Is(err, ErrClaimed):
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got, _ := s.Get(l.ID); wins.Load() != 1 || len(got.Devices) != 1 {
		t.Fatalf("%d claims won, %d devices", wins.Load(), len(got.Devices))
	}
}

func TestRemoveAndResetDevices(t *testing.T) {
	s := newStore(t, t.TempDir())
	l := mustCreate(t, s, draft(), "")
	a, _ := s.AddDevice(l.ID, newDevice(model.ApprovedByAdmin), maxDev)
	b, _ := s.AddDevice(l.ID, newDevice("device:"+a.ID), maxDev)
	if err := s.RemoveDevice(l.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	// b names a gone approver; the link still saves.
	if s.HasDevice(l.ID, a.ID) || !s.HasDevice(l.ID, b.ID) {
		t.Error("remove hit the wrong device")
	}
	if err := s.RemoveDevice(l.ID, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove twice: %v", err)
	}
	if _, err := s.Update(l.ID, draft()); err != nil {
		t.Errorf("edit after the approver went: %v", err)
	}
	if err := s.ResetDevices(l.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(l.ID); len(got.Devices) != 0 {
		t.Errorf("devices after reset: %+v", got.Devices)
	}
	if err := s.ResetDevices("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("reset unknown link: %v", err)
	}
}

// fileStamp changes when links.yaml is saved.
func fileStamp(t *testing.T, dir string) string {
	t.Helper()
	st, err := os.Stat(filepath.Join(dir, migrate.LinksFile))
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(st.ModTime().UnixNano(), st.Size())
}

func TestTouchDeviceThrottles(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	l := mustCreate(t, s, draft(), "")
	d, _ := s.AddDevice(l.ID, newDevice(model.ApprovedByAdmin), maxDev)
	seen := func() time.Time {
		got, _ := newStore(t, dir).Get(l.ID)
		dev, _ := got.Device(d.ID)
		return dev.LastSeenAt
	}
	if err := s.TouchDevice(l.ID, d.ID, t0.Add(time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	if !seen().Equal(t0.Add(time.Second)) {
		t.Fatalf("last seen = %v", seen())
	}
	stamp := fileStamp(t, dir)
	// Inside the minute: no write.
	for i := range 50 {
		if err := s.TouchDevice(l.ID, d.ID, t0.Add(time.Second+time.Duration(i)*time.Second), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if fileStamp(t, dir) != stamp || !seen().Equal(t0.Add(time.Second)) {
		t.Error("touch inside the minute wrote")
	}
	if err := s.TouchDevice(l.ID, d.ID, t0.Add(time.Minute+time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	if !seen().Equal(t0.Add(time.Minute + time.Second)) {
		t.Errorf("touch after a minute did not write: %v", seen())
	}
}

// A touch never brings back a removed device, even inside the throttle.
func TestTouchRemovedDevice(t *testing.T) {
	s := newStore(t, t.TempDir())
	l := mustCreate(t, s, draft(), "")
	d, _ := s.AddDevice(l.ID, newDevice(model.ApprovedByAdmin), maxDev)
	if err := s.TouchDevice(l.ID, d.ID, t0, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveDevice(l.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchDevice(l.ID, d.ID, t0.Add(time.Second), time.Minute); !errors.Is(err, ErrNotFound) {
		t.Errorf("touch removed device: %v", err)
	}
	if s.HasDevice(l.ID, d.ID) {
		t.Error("touch re-added the device")
	}
	if err := s.TouchDevice("nope", d.ID, t0, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Errorf("touch on unknown link: %v", err)
	}
}

// The cap comes from the caller (an app option). Lowering it keeps the
// devices a link has and refuses new ones.
func TestAddDeviceLoweredCap(t *testing.T) {
	s := newStore(t, t.TempDir())
	l := mustCreate(t, s, draft(), "")
	for range 3 {
		if _, err := s.AddDevice(l.ID, newDevice(model.ApprovedByAdmin), 3); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddDevice(l.ID, newDevice(model.ApprovedByAdmin), 2); !errors.Is(err, ErrTooManyDevices) {
		t.Errorf("add over a lowered cap: %v", err)
	}
	if _, err := s.Update(l.ID, draft()); err != nil {
		t.Errorf("edit of a link over the cap: %v", err)
	}
	if got, _ := s.Get(l.ID); len(got.Devices) != 3 {
		t.Errorf("devices = %d", len(got.Devices))
	}
}

// The throttle comes from the caller too.
func TestTouchDeviceThrottleOption(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	l := mustCreate(t, s, draft(), "")
	d, _ := s.AddDevice(l.ID, newDevice(model.ApprovedByAdmin), maxDev)
	_ = s.TouchDevice(l.ID, d.ID, t0, 5*time.Second)
	_ = s.TouchDevice(l.ID, d.ID, t0.Add(6*time.Second), 5*time.Second)
	got, _ := newStore(t, dir).Get(l.ID)
	if dev, _ := got.Device(d.ID); !dev.LastSeenAt.Equal(t0.Add(6 * time.Second)) {
		t.Errorf("last seen = %v with a 5 s throttle", dev.LastSeenAt)
	}
}
