package config

import "os"

// CanonicalEnv reads the pre-release TPA.run environment surface.
func CanonicalEnv(name string) string { return os.Getenv(name) }

func HasCanonicalEnv(name string) bool {
	_, ok := os.LookupEnv(name)
	return ok
}
