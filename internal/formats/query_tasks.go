package formats

// Query task metadata shares one bounded history policy across ordinary,
// cached and recoverable starts. Result/source leases have separate limits.
const QueryTaskHistory = 60
