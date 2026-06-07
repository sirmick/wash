//go:build multicall && !no_app_audio

package main

// Register the wash-audio control-plane service (com.wash.audio) into
// the multicall binary. docs/AUDIO.md §3.
import _ "github.com/sirmick/wash/apps/audio/be"
