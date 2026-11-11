package web

import "time"

// TLSStatus is the HTTPS state of the guest port, as GET /api/settings
// shows it. It holds no key material.
type TLSStatus struct {
	// Enabled is the ssl option.
	Enabled bool `json:"enabled"`
	// Source is "files" or "self-signed" while Enabled, else "".
	Source string `json:"source,omitempty"`
	// Fingerprint is the SHA-256 of the certificate in use.
	Fingerprint string    `json:"fingerprint,omitempty"`
	NotAfter    time.Time `json:"not_after,omitzero"`
	// CertFile and KeyFile are the file options, names in /ssl.
	CertFile string `json:"certfile"`
	KeyFile  string `json:"keyfile"`
	// SelfSignedDays is the ssl_self_signed_days option.
	SelfSignedDays int `json:"self_signed_days"`
}

// tlsStatus returns the guest port's TLS state. Without a TLSStatus dep,
// HTTPS is off.
func (s *Server) tlsStatus() TLSStatus {
	if s.deps.TLSStatus == nil {
		o := s.deps.Options.TLS
		return TLSStatus{CertFile: o.CertFile, KeyFile: o.KeyFile, SelfSignedDays: int(o.SelfSignedLifetime / (24 * time.Hour))}
	}
	return s.deps.TLSStatus()
}
