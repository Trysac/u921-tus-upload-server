// Package tusuploadserver is a small, dependency-free Go library: Resumable upload server implementing the tus 1.0.0 protocol with creation, termination, expiration and checksum extensions on disk
//
// The public API is every exported identifier of this root package. Functions are pure: they do
// no I/O, keep no global mutable state and report invalid input through returned errors.
package tusuploadserver
