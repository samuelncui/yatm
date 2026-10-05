package library

import (
	"strings"

	"github.com/samuelncui/yatm/entity"
)

// DefaultLocationIgnore keeps YATM-managed entries out of ordinary Location content by
// default. It is ordinary gitignore text in the Location's own Ignore, not a separate
// exclusion mechanism: the operator can edit or remove any rule.
const DefaultLocationIgnore = `# YATM-managed entries. Edit or remove these rules to index them.
.yatm.json
.yatm-trash
.yatm-restore-*
.yatm-fileops-*
`

// WithDefaultLocationIgnore starts a registration or an imported catalog from the default
// YATM entry rules when it carries no rules of its own. Authored text is never rewritten.
func WithDefaultLocationIgnore(exclusions *entity.IgnoreRules) *entity.IgnoreRules {
	if strings.TrimSpace(exclusions.GetText()) != "" {
		return exclusions
	}
	return &entity.IgnoreRules{Format: "gitignore", Text: DefaultLocationIgnore}
}
