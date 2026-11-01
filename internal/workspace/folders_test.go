package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestFolderEditsPreserveMissingEntriesAndUnknownSettings(t *testing.T) {
	s, p := fixture(t)
	write(t, s.RegistryPath(), `{"future":{"enabled":true},"projects":[{"id":"demo","name":"Custom name","path":`+quotedJSON(p.Path)+`,"launch":["echo","hello"],"extra":{"keep":42}},{"id":"missing","path":"~/offline","extra":"retain"}]}`)
	var before map[string]json.RawMessage
	b, _ := os.ReadFile(s.RegistryPath())
	if err := json.Unmarshal(b, &before); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(s.Home, "folder with spaces")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	added, err := s.AddFolder("~/folder with spaces")
	if err != nil || added.Path != outside || added.Name != "folder with spaces" {
		t.Fatal(added, err)
	}
	if err = s.RemoveFolder(added.ID); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(s.RegistryPath())
	var after map[string]json.RawMessage
	if err = json.Unmarshal(b, &after); err != nil {
		t.Fatal(err)
	}
	delete(after, "reserved_project_ids")
	var expected, actual any
	oldJSON, _ := json.Marshal(before)
	json.Unmarshal(oldJSON, &expected)
	newJSON, _ := json.Marshal(after)
	json.Unmarshal(newJSON, &actual)
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("folder edits lost settings:\n%s", b)
	}
	if _, err = os.Stat(outside); err != nil {
		t.Fatal("removing registration touched the folder", err)
	}
}

func TestRemovedFolderIdentityCannotBeReused(t *testing.T) {
	s, _ := fixture(t)
	first := filepath.Join(s.Home, "a", "server")
	second := filepath.Join(s.Home, "b", "server")
	for _, path := range []string{first, second} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.AddFolder(first)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RemoveFolder(p.ID); err != nil {
		t.Fatal(err)
	}
	other, err := s.AddFolder(second)
	if err != nil || other.ID == p.ID {
		t.Fatal("removed identity was reassigned to a different folder", other, err)
	}
	// The reservation survives loading a fresh service and needs no live run.
	s2 := s
	again, err := s2.AddFolder(first)
	if err != nil || again.ID == p.ID || again.ID == other.ID {
		t.Fatal("removed identity was recycled after reload", again, err)
	}
	discovered := filepath.Join(s.ProjectsRoot, "server")
	if err := os.MkdirAll(discovered, 0700); err != nil {
		t.Fatal(err)
	}
	r, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range r.Projects {
		if item.Path == discovered && (item.ID == p.ID || item.ID == other.ID || item.ID == again.ID) {
			t.Fatal("discovery recycled a reserved identity", item)
		}
	}
}

func quotedJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestAddFolderDeduplicatesAliasesAndReservesAllIDs(t *testing.T) {
	s, p := fixture(t)
	outside := filepath.Join(s.Home, "demo")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	added, err := s.AddFolder(outside)
	if err != nil || added.ID != "demo-2" {
		t.Fatal(added, err)
	}
	alias := filepath.Join(s.Home, "alias")
	if err = os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.RegistryPath())
	duplicate, err := s.AddFolder(alias)
	if err != nil || duplicate.ID != added.ID {
		t.Fatal(duplicate, err)
	}
	discovered, err := s.AddFolder(p.Path)
	if err != nil || discovered.ID != p.ID {
		t.Fatal(discovered, err)
	}
	after, _ := os.ReadFile(s.RegistryPath())
	if string(before) != string(after) {
		t.Fatal("duplicate add rewrote the registry")
	}
	if err = s.RemoveFolder(p.ID); err == nil {
		t.Fatal("automatically discovered folder was removable")
	}
	if err = os.Remove(outside); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(s.Home, "another", "demo")
	if err = os.MkdirAll(other, 0700); err != nil {
		t.Fatal(err)
	}
	next, err := s.AddFolder(other)
	if err != nil || next.ID != "demo-3" {
		t.Fatal("missing folder ID was reused", next, err)
	}
}

func TestFolderValidationAndConcurrentAdds(t *testing.T) {
	s, _ := fixture(t)
	file := filepath.Join(s.Home, "file")
	write(t, file, "keep")
	for _, path := range []string{"", " ", "relative/path", "~/missing", file} {
		if _, err := s.AddFolder(path); err == nil {
			t.Fatalf("invalid folder accepted: %q", path)
		}
	}
	path := filepath.Join(s.Home, "parallel")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := s.AddFolder(path); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	r, err := s.Load()
	if err != nil || len(r.Projects) != 2 {
		t.Fatal("concurrent adds duplicated or lost folders", r, err)
	}
	before, _ := os.ReadFile(s.RegistryPath())
	if err = s.RemoveFolder("unknown"); err == nil {
		t.Fatal("unknown folder removed")
	}
	after, _ := os.ReadFile(s.RegistryPath())
	if string(before) != string(after) || !strings.Contains(string(after), "parallel") {
		t.Fatal("invalid removal changed the registry")
	}
}
