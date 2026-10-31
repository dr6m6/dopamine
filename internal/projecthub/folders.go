package projecthub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Read the saved document, rather than Load's filtered discovery result. Edits
// retain missing folders and unknown settings from older/newer versions.
func (s Service) folderDocument() (map[string]json.RawMessage, []json.RawMessage, Registry, error) {
	doc := map[string]json.RawMessage{}
	b, err := os.ReadFile(s.RegistryPath())
	if errors.Is(err, os.ErrNotExist) {
		return doc, nil, Registry{}, nil
	}
	if err != nil {
		return nil, nil, Registry{}, err
	}
	r, err := s.parseRegistry(b)
	if err != nil {
		return nil, nil, r, err
	}
	if err = json.Unmarshal(b, &doc); err != nil || doc == nil {
		return nil, nil, r, errors.New("projects.json requires an object")
	}
	var entries []json.RawMessage
	if raw, ok := doc["projects"]; ok {
		if err = json.Unmarshal(raw, &entries); err != nil {
			return nil, nil, r, err
		}
	}
	return doc, entries, r, nil
}

func (s Service) saveFolderDocument(doc map[string]json.RawMessage, entries []json.RawMessage) error {
	if entries == nil {
		entries = []json.RawMessage{}
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	doc["projects"] = b
	return AtomicJSON(s.RegistryPath(), doc)
}

func reservedFolderIDs(doc map[string]json.RawMessage) ([]string, error) {
	var ids []string
	if raw, ok := doc["reserved_project_ids"]; ok {
		if err := json.Unmarshal(raw, &ids); err != nil {
			return nil, fmt.Errorf("reserved_project_ids: %w", err)
		}
	}
	return ids, nil
}

// AddFolder registers an existing directory. Aliases select the existing entry;
// no files inside the folder are created or changed.
func (s Service) AddFolder(path string) (Project, error) {
	if strings.TrimSpace(path) == "" {
		return Project{}, errors.New("enter a folder path")
	}
	path = s.Expand(path)
	if !filepath.IsAbs(path) {
		return Project{}, errors.New("use an absolute path or ~/folder")
	}
	info, err := os.Stat(path)
	if err != nil {
		return Project{}, err
	}
	if !info.IsDir() {
		return Project{}, errors.New("path is not a folder")
	}
	lock, err := s.lock()
	if err != nil {
		return Project{}, err
	}
	defer lock.Close()
	doc, entries, saved, err := s.folderDocument()
	if err != nil {
		return Project{}, err
	}
	visible := s.discover(saved)
	key := projectPathKey(path)
	ids := map[string]bool{}
	reserved, err := reservedFolderIDs(doc)
	if err != nil {
		return Project{}, err
	}
	for _, id := range reserved {
		ids[id] = true
	}
	for _, p := range saved.Projects {
		ids[p.ID] = true
	}
	for _, p := range visible.Projects {
		if projectPathKey(p.Path) == key {
			return p, nil
		}
		ids[p.ID] = true
	}
	name := filepath.Base(path)
	base := strings.Trim(regexp.MustCompile(`[^A-Za-z0-9_-]`).ReplaceAllString(name, "-"), "-")
	if base == "" {
		base = "folder"
	}
	id := base
	for suffix := 2; ids[id]; suffix++ {
		id = fmt.Sprintf("%s-%d", base, suffix)
	}
	p := Project{ID: id, Name: name, Path: path}
	raw, err := json.Marshal(p)
	if err != nil {
		return Project{}, err
	}
	if err = s.saveFolderDocument(doc, append(entries, raw)); err != nil {
		return Project{}, err
	}
	return p, nil
}

// ExternalFolder reports whether removing its registration can hide the folder.
// Ordinary immediate children of the discovery root are always rediscovered.
func (s Service) ExternalFolder(path string) bool {
	key, root := projectPathKey(path), projectPathKey(s.ProjectsRoot)
	if filepath.Dir(key) != root {
		return true
	}
	name := filepath.Base(key)
	if strings.HasPrefix(name, ".") || strings.EqualFold(name, "octo") || name == "lost+found" {
		return true
	}
	info, err := os.Lstat(filepath.Join(root, name))
	return err != nil || !info.IsDir()
}

func (s Service) RemoveFolder(id string) error {
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	doc, entries, saved, err := s.folderDocument()
	if err != nil {
		return err
	}
	for i, p := range saved.Projects {
		if p.ID != id {
			continue
		}
		if !s.ExternalFolder(p.Path) {
			return errors.New("this folder is discovered automatically")
		}
		// Open processes and history still refer to this identity. Never give
		// it to a different folder, even after old runtime records are cleaned.
		reserved, err := reservedFolderIDs(doc)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(append(reserved, p.ID))
		if err != nil {
			return err
		}
		doc["reserved_project_ids"] = raw
		return s.saveFolderDocument(doc, append(entries[:i], entries[i+1:]...))
	}
	return fmt.Errorf("unknown folder %q", id)
}
