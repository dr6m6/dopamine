package workspace

import (
	"encoding/json"
	"fmt"
)

// Change a single field without losing unknown fields or hidden projects.
func (s Service) setProjectField(p Project, field string, value any) error {
	doc, entries, saved, err := s.folderDocument()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	for i, item := range saved.Projects {
		if item.ID != p.ID {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entries[i], &fields); err != nil {
			return err
		}
		fields[field] = raw
		entries[i], err = json.Marshal(fields)
		if err != nil {
			return err
		}
		return s.saveFolderDocument(doc, entries)
	}
	// Auto-discovered projects need their first saved entry.
	fields, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var item map[string]json.RawMessage
	if err = json.Unmarshal(fields, &item); err != nil {
		return err
	}
	item[field] = raw
	entry, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return s.saveFolderDocument(doc, append(entries, entry))
}

func (s Service) SetCloudEnvironment(p Project, environment string) error {
	if environment == "" {
		return fmt.Errorf("enter a cloud environment name or id")
	}
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	return s.setProjectField(p, "cloud_environment", environment)
}
