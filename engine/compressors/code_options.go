package compressors

// CommentMode controls how the code compressor treats comments and docstrings.
// The zero value (CommentKeep) preserves the historical behavior: only function
// bodies are elided and comments/docstrings are left untouched.
type CommentMode int

const (
	// CommentKeep leaves comments / docstrings untouched (default).
	CommentKeep CommentMode = iota
	// CommentRemove elides comment (and, for Docstrings, Python docstring) nodes
	// entirely. Removal is always followed by the compressor's re-parse gate, so
	// any removal that would break the parse falls back to pass-through.
	CommentRemove
)

// CodeOptions configures the code compressor. The zero value is the historical
// behavior (bodies elided, comments and docstrings kept). A FirstLine mode is
// intentionally not offered yet: trimming a comment/docstring to its first line
// requires language-specific quote-style surgery that is high-risk for the
// marginal token win, and the re-parse gate cannot catch a semantically-wrong
// (but still parseable) truncation.
type CodeOptions struct {
	// Comments controls non-docstring comment nodes (all supported languages).
	Comments CommentMode
	// Docstrings controls Python module/class docstrings (string-literal
	// statements). Brace languages have no docstrings; this is a no-op for them.
	Docstrings CommentMode
}
