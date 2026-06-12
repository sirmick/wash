//go:build multicall && !no_app_music

package main

// Register the windowed wash-music app (com.wash.music) into the
// multicall binary. docs/AUDIO.md §2.
import _ "github.com/sirmick/wash/apps/music/be"
