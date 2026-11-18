package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type usersFile struct {
	Users []struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	} `json:"users"`
}

// loadUsers reads a users file. Supported formats:
//   - JSON object: {"users":[{"uuid":"...","name":"..."}]}  (s-ui export style)
//   - JSON array:  ["uuid1","uuid2",...]
//   - plain text:  one uuid per line, '#' comments allowed
func loadUsers(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(raw))
	var out []string
	if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
		if strings.HasPrefix(s, "{") {
			var uf usersFile
			if err = json.Unmarshal([]byte(s), &uf); err != nil {
				return nil, fmt.Errorf("users file: %w", err)
			}
			for _, u := range uf.Users {
				out = append(out, strings.ToLower(strings.TrimSpace(u.UUID)))
			}
		} else {
			var arr []string
			if err = json.Unmarshal([]byte(s), &arr); err != nil {
				return nil, fmt.Errorf("users file: %w", err)
			}
			for _, u := range arr {
				out = append(out, strings.ToLower(strings.TrimSpace(u)))
			}
		}
	} else {
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			out = append(out, strings.ToLower(line))
		}
	}
	clean := out[:0:0]
	for _, u := range out {
		if u != "" && u != "null" {
			clean = append(clean, u)
		}
	}
	out = clean
	if len(out) == 0 {
		return nil, fmt.Errorf("users file %s: no users", path)
	}
	return out, nil
}
