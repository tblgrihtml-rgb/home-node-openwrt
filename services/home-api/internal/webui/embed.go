package webui

import "embed"

// Files contains the browser UI served by Home API.
//
//go:embed static/*
var Files embed.FS
