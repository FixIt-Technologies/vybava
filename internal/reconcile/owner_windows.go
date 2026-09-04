//go:build windows

package reconcile

func ownerOf(string) string { return "" }

func currentUser() string { return "" }
