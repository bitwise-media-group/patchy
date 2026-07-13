// The e2e suite is a separate module so its harness dependencies never reach
// the product binaries.
module github.com/bitwise-media-group/patchy/e2e

go 1.26.5

replace github.com/bitwise-media-group/patchy => ..
