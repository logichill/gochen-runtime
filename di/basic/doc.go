// Package basic provides the default reflection-based DI container.
//
// Circular dependency diagnostics cover constructor dependency cycles and
// cross-goroutine singleton resolution waits.
package basic
