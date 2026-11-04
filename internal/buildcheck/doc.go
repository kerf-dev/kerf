// Package buildcheck holds tests that guard the build and deployment files against drift from the
// Go code: every binary's Dockerfile copies the module packages it imports, and dev service URLs
// point at the services the make targets install. It has no code.
package buildcheck
