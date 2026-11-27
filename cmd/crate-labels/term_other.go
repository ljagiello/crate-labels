//go:build !darwin

package main

import "os"

// readHidden cannot turn echo off on this platform; the caller falls back to a plain read.
func readHidden(*os.File) (string, error) { return "", errNotTerminal }
