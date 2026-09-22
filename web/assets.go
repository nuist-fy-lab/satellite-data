package web

import "embed"

// Files contains the dependency-free web interface served by satellited.
//
//go:embed static/*
var Files embed.FS
