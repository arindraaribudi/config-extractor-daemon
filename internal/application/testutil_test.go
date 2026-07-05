package application

import "os"

// osReadFile is a tiny indirection so tests in this package can read
// child-process output without pulling os into every test file.
func osReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
