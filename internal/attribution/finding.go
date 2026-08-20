package attribution

// Finding is a condition worth reporting through devreap doctor. Attribution
// components never fail loudly on a degraded input: they carry on with what
// still works and record why, because a broken data file must not stop the
// tool from running.
type Finding struct {
	// Kind is a stable identifier for the condition, suitable for matching.
	Kind string `json:"kind"`
	// Detail explains this occurrence in one line, for a person to read.
	Detail string `json:"detail"`
}

// Finding kinds raised by this package.
const (
	// FindingAdapterFileUnreadable means a user adapter file could not be read
	// or parsed. The built-in descriptors stay in force.
	FindingAdapterFileUnreadable = "adapter_file_unreadable"
	// FindingAdapterDescriptorRejected means one user descriptor failed
	// load-time validation and was skipped. The rest of the file still loaded.
	FindingAdapterDescriptorRejected = "adapter_descriptor_rejected"
)
