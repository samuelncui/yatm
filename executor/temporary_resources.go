package executor

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ProtectTemporaryNames excludes one attempt's unpredictable directory prefix from live original readers.
func (e *Executor) ProtectTemporaryNames(prefix string) (func(), error) {
	// Register before creating temporary data; live access and Analyze consult this registry dynamically.
	allowed := strings.HasPrefix(prefix, ".yatm-restore-") || strings.HasPrefix(prefix, ".yatm-fileops-")
	if !allowed || len(prefix) < 30 || filepath.Base(prefix) != prefix {
		return nil, fmt.Errorf("invalid temporary resource prefix")
	}
	e.temporaryNamesLock.Lock()
	defer e.temporaryNamesLock.Unlock()
	if e.temporaryNames == nil {
		e.temporaryNames = make(map[string]struct{})
	}
	if _, exists := e.temporaryNames[prefix]; exists {
		return nil, fmt.Errorf("temporary resource prefix is already registered")
	}
	e.temporaryNames[prefix] = struct{}{}
	return func() {
		e.temporaryNamesLock.Lock()
		defer e.temporaryNamesLock.Unlock()
		delete(e.temporaryNames, prefix)
	}, nil
}

func (e *Executor) isTemporaryResource(filename string) bool {
	// Match only active random names, not ordinary dot directories or a blanket work-directory exclusion.
	e.temporaryNamesLock.RLock()
	defer e.temporaryNamesLock.RUnlock()
	for _, component := range strings.Split(filepath.ToSlash(filename), "/") {
		for prefix := range e.temporaryNames {
			if strings.HasPrefix(component, prefix) {
				return true
			}
		}
	}
	return false
}
