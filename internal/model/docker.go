package model

import "time"

// DockerTLS is the optional client TLS material for a Docker host, as PEM.
// It is stored encrypted and never returned by the API.
type DockerTLS struct {
	CA   string `json:"ca,omitempty"`
	Cert string `json:"cert,omitempty"`
	Key  string `json:"key,omitempty"`
}

// Empty reports whether no TLS material is set.
func (t DockerTLS) Empty() bool { return t == DockerTLS{} }

// DockerHost is a Docker Engine API endpoint a team's docker monitors read
// from: a socket proxy URL, a unix socket, or a remote host over TCP/TLS.
type DockerHost struct {
	ID       int64
	TeamID   int64
	Name     string
	Endpoint string // tcp://host:port, http(s)://host:port or unix:///path
	TLS      DockerTLS
	// DecryptErr is set when the TLS material could not be decrypted
	// (APP_SECRET changed); TLS is then empty.
	DecryptErr string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
