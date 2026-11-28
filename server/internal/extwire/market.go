package extwire

// MarketPackage is an immutable package reference used in a marketplace catalog.
type MarketPackage struct {
	// Platforms are the GOOS-GOARCH it runs on. An empty list matches any platform.
	Platforms []string `json:"platforms,omitempty"`
	// URL is relative to the index, an HTTPS or OCI reference, or a GitHub release asset.
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
}
