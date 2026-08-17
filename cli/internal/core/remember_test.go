package core

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestRememberPortIsIdempotentAndPreservesOthers(t *testing.T) {
	existing := []ForwardingPolicy{{
		ID:         "db",
		Priority:   5,
		Action:     PolicyIgnore,
		Conditions: []PolicyCondition{{RemotePorts: &PortRange{From: 5432, To: 5432}}},
	}}
	first, added := RememberPort(existing, 5173)
	if !added || len(first) != 2 {
		t.Fatalf("RememberPort = %#v, %v, want append", first, added)
	}
	if existing[0].ID != "db" || len(existing) != 1 {
		t.Fatalf("RememberPort mutated the caller slice: %#v", existing)
	}
	second, added := RememberPort(first, 5173)
	if added {
		t.Fatalf("second RememberPort added again: %#v", second)
	}
	want := []ForwardingPolicy{
		existing[0],
		{
			ID:       "port-5173",
			Priority: rememberedPolicyPriority,
			Action:   PolicyAutoForward,
			Conditions: []PolicyCondition{{
				RemotePorts: &PortRange{From: 5173, To: 5173},
			}},
		},
	}
	if diff := cmp.Diff(first, want); diff != "" {
		t.Fatalf("remembered policies mismatch (-got +want):\n%s", diff)
	}
}

func TestForgetPortLeavesComplexPolicy(t *testing.T) {
	complex := ForwardingPolicy{
		ID:     "web",
		Action: PolicyAutoForward,
		Conditions: []PolicyCondition{{
			RemotePorts: &PortRange{From: 5173, To: 5173},
			Executable:  strptr("node"),
		}},
	}
	policies := []ForwardingPolicy{complex}
	got, removed := ForgetPort(policies, 5173)
	if removed || len(got) != 1 || got[0].ID != "web" {
		t.Fatalf("ForgetPort = %#v, %v, want the complex rule kept", got, removed)
	}
}

func TestRememberAndForgetDirectory(t *testing.T) {
	added, stored, changed, err := RememberDirectory(nil, "/home/dev/src/app/")
	if err != nil || !changed || stored != "/home/dev/src/app" || len(added) != 1 {
		t.Fatalf("RememberDirectory = %#v, %q, %v, %v", added, stored, changed, err)
	}
	if added[0].ID != "dir-/home/dev/src/app" {
		t.Fatalf("id = %q", added[0].ID)
	}
	again, stored, changed, err := RememberDirectory(added, "/home/dev/src/app")
	if err != nil || changed || stored != "/home/dev/src/app" || len(again) != 1 {
		t.Fatalf("idempotent RememberDirectory = %#v, %q, %v, %v", again, stored, changed, err)
	}
	forgotten, stored, changed, err := ForgetDirectory(added, "/home/dev/src/app/")
	if err != nil || !changed || stored != "/home/dev/src/app" || len(forgotten) != 0 {
		t.Fatalf("ForgetDirectory = %#v, %q, %v, %v", forgotten, stored, changed, err)
	}
}

func TestRememberDirectoryRejectsRelativePath(t *testing.T) {
	_, _, _, err := RememberDirectory(nil, "src/app")
	if !errors.Is(err, ErrHostDirectory) {
		t.Fatalf("err = %v, want ErrHostDirectory", err)
	}
	_, _, _, err = RememberDirectory(nil, "  ")
	if !errors.Is(err, ErrEmptyDirectory) {
		t.Fatalf("err = %v, want ErrEmptyDirectory", err)
	}
}
