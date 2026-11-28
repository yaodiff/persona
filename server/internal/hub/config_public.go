package hub

// PublicConfig is how the hub is reached from outside its LAN (docs/archive/redesign/hub-changes.md §1): the TLS listener,
// the addresses it hands out, and how it treats plaintext requests from the internet. Empty means the hub behaves
// as before: plaintext on Listen only.
type PublicConfig struct {
	TLSListen string   `json:"tls_listen,omitempty"`
	CertFile  string   `json:"cert_file,omitempty"`
	KeyFile   string   `json:"key_file,omitempty"`
	Advertise []string `json:"advertise,omitempty"`
	// LinkAdvertise names HTTPS endpoints that serve the hub's pinned certificate.
	LinkAdvertise []string `json:"link_advertise,omitempty"`
	// Nil means enabled, including when an older hub.json has no public section.
	AdvertiseIPv6 *bool `json:"advertise_ipv6,omitempty"`
	AdvertiseLAN  *bool `json:"advertise_lan,omitempty"`

	DDNS *DDNSConfig `json:"ddns,omitempty"`
}
