package client

import "testing"

func storages() []*NodeStorage {
	return []*NodeStorage{
		{Storage: "local-lvm", Type: "lvmthin", Content: "images,rootdir", Active: 1},
		{Storage: "local", Type: "dir", Content: "snippets,vztmpl,iso", Active: 1},
		{Storage: "cephfs", Type: "cephfs", Content: "snippets,backup", Active: 1},
	}
}

func TestPickSnippetsStorage_PrefersConfiguredWhenCapable(t *testing.T) {
	got, ok := PickSnippetsStorage(storages(), "cephfs")
	if !ok || got != "cephfs" {
		t.Errorf("got (%q,%v), want (cephfs,true) — configured snippets-capable storage preferred", got, ok)
	}
}

func TestPickSnippetsStorage_FallsBackWhenPreferredNotCapable(t *testing.T) {
	// Preferred is the LVM disk store (no snippets) → fall back to the first
	// snippets-capable, sorted by ID: "cephfs" < "local".
	got, ok := PickSnippetsStorage(storages(), "local-lvm")
	if !ok || got != "cephfs" {
		t.Errorf("got (%q,%v), want (cephfs,true) — first snippets-capable by ID", got, ok)
	}
}

func TestPickSnippetsStorage_FallsBackWhenNoPreferred(t *testing.T) {
	got, ok := PickSnippetsStorage(storages(), "")
	if !ok || got != "cephfs" {
		t.Errorf("got (%q,%v), want (cephfs,true)", got, ok)
	}
}

func TestPickSnippetsStorage_NoneCapable(t *testing.T) {
	only := []*NodeStorage{
		{Storage: "local-lvm", Type: "lvmthin", Content: "images,rootdir", Active: 1},
	}
	if got, ok := PickSnippetsStorage(only, "local-lvm"); ok {
		t.Errorf("got (%q,true), want (\"\",false) — LVM-only node has no snippets storage", got)
	}
}
