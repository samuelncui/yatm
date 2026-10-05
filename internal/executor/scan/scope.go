package scan

// Scope describes one selected range during an execution attempt.
type Scope struct {
	ID         int64
	LocationID int64
	Path       string
}
