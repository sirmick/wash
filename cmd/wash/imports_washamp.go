//go:build multicall && !no_app_music

package main

// Register the windowed wash-washamp app (com.wash.washamp) into the
// multicall binary. docs/AUDIO.md §2.
import _ "github.com/sirmick/wash/apps/washamp/be"
