package main

import "os"

func tparunEnv(name string) string { return os.Getenv(name) }
