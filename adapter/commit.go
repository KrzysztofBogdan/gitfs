package adapter

// CommitKind discriminates content sends from deletions.
type CommitKind int

const (
	// CommitContent is a send with new working-tree contents.
	CommitContent CommitKind = iota
	// CommitDeletion is a request to delete a path on the service.
	CommitDeletion
)

// CommitRequest is a single path the core hands to Adapter.Commit.
// Content is populated for CommitContent; it is nil for CommitDeletion.
// Message is the verbatim value of `-m` from the invocation (may be "").
type CommitRequest struct {
	Path    string
	Kind    CommitKind
	Content []byte
	Message string
}

// CommitResult describes one canonical post-commit path the adapter
// returned for an accepted request. A result at the same path as the
// request represents normalization (server-filled fields, canonical
// form); a result at a different path represents a rename or
// derivation. Delete=true removes the path; Content is ignored.
type CommitResult struct {
	Path    string
	Content []byte
	Delete  bool
}

// CommitEmitter is the adapter→core callback during Commit. For every
// request passed in, the adapter calls exactly one of Accept (the send
// succeeded) or Reject (the service refused the change). Neither call
// mutates the workdir directly — the core applies the canonical state
// after the Commit call returns.
//
// UpdateCredentials lets an adapter round-trip a revised credentials
// blob after it has successfully authenticated against a lazily-
// discovered endpoint (e.g. SMTP host discovery for imap://). The core
// persists the new blob to the keyring when it came from the keyring;
// env-derived credentials are never re-persisted. Calling with nil is
// equivalent to not calling.
type CommitEmitter interface {
	Accept(inputPath string, results []CommitResult) error
	Reject(inputPath string, reason string) error
	UpdateCredentials(blob Credentials) error
}
